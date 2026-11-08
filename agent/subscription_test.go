package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeSubscriptionRetainsOnlyValidatedNodes(t *testing.T) {
	body := []byte("external-controller: 0.0.0.0:9999\nproxies:\n  - name: test-node\n    type: ss\n    server: example.invalid\n    port: 443\n    cipher: aes-128-gcm\n    password: fixture-password\n")
	normalized, count, err := NormalizeSubscription(body)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("node count=%d", count)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(normalized, &value); err != nil {
		t.Fatal("normalized cache is not JSON")
	}
	if len(value) != 1 || value["proxies"] == nil {
		t.Fatal("subscription configuration escaped the node-only boundary")
	}
}

func TestNormalizeRejectsEmptyDuplicateAndAliasedNodes(t *testing.T) {
	for _, body := range []string{
		"proxies: []", "message: subscription expired",
		"proxies: [{name: duplicate, type: ss}, {name: duplicate, type: ss}]",
		"proxies: [&node {name: anchored, type: ss}, *node]",
	} {
		if _, _, err := NormalizeSubscription([]byte(body)); err == nil {
			t.Errorf("accepted invalid provider %q", body)
		}
	}
}

func TestNormalizeEnforcesNodeAndBodyLimits(t *testing.T) {
	nodes := make([]map[string]any, 1001)
	for i := range nodes {
		nodes[i] = map[string]any{"name": fmt.Sprint(i), "type": "ss"}
	}
	body, _ := json.Marshal(map[string]any{"proxies": nodes})
	if _, _, err := NormalizeSubscription(body); err == nil {
		t.Fatal("accepted more than 1000 nodes in one source")
	}
	if _, _, err := NormalizeSubscription(make([]byte, 4*1024*1024+1)); err == nil {
		t.Fatal("accepted an oversized source")
	}
}

func TestSubscriptionURLRejectsNonHTTPAndMalformedInput(t *testing.T) {
	for _, value := range []string{"file:///etc/shadow", "gopher://example.com/a", "http://", "https://example.com/\nsecret"} {
		if ValidateSubscriptionURL(value) == nil {
			t.Errorf("accepted invalid URL %q", value)
		}
	}
	for _, value := range []string{"https://sub.example/config?token=private", "http://192.168.31.2/sub.yaml"} {
		if err := ValidateSubscriptionURL(value); err != nil {
			t.Errorf("rejected valid subscription: %v", err)
		}
	}
}

func TestSubscriptionListMaskDoesNotRevealCredentialsOrPath(t *testing.T) {
	masked := MaskSubscriptionURL("https://user:password@sub.example/private-token?token=secret-query")
	for _, secret := range []string{"user", "password", "private-token", "secret-query"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("masked subscription disclosed %q", secret)
		}
	}
	if !strings.Contains(masked, "sub.example") {
		t.Fatal("masked URL lost its useful host label")
	}
}
