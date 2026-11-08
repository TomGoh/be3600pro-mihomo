package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

func ValidateSubscriptionURL(raw string) error {
	if len(raw) == 0 || len(raw) > 4096 || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return errors.New("订阅地址格式不正确")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Fragment != "" {
		return errors.New("仅支持 HTTP 或 HTTPS 订阅地址")
	}
	return nil
}
func MaskSubscriptionURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "已保存的订阅"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

func NormalizeSubscription(body []byte) ([]byte, int, error) {
	if len(body) == 0 || len(body) > 4*1024*1024 {
		return nil, 0, errors.New("订阅为空或超过 4 MiB")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&document); err != nil {
		return nil, 0, errors.New("订阅不是有效的 Clash/Mihomo YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, 0, errors.New("不支持多个 YAML 文档")
	}
	type entry struct {
		node  *yaml.Node
		depth int
	}
	stack := []entry{{&document, 0}}
	count := 0
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		count++
		if count > 80000 || e.depth > 32 || e.node.Kind == yaml.AliasNode {
			return nil, 0, errors.New("订阅含引用或结构过于复杂，请使用普通 Clash 节点订阅")
		}
		for _, child := range e.node.Content {
			stack = append(stack, entry{child, e.depth + 1})
		}
	}
	var source struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := document.Decode(&source); err != nil {
		return nil, 0, errors.New("订阅节点结构不正确")
	}
	if len(source.Proxies) == 0 || len(source.Proxies) > 1000 {
		return nil, 0, errors.New("单份订阅须包含 1 至 1000 个节点")
	}
	names := map[string]bool{}
	for _, proxy := range source.Proxies {
		name, ok := proxy["name"].(string)
		typeName, typeOK := proxy["type"].(string)
		if !ok || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 || names[name] || !typeOK || typeName == "" {
			return nil, 0, errors.New("订阅节点名称或类型无效，或存在重名")
		}
		names[name] = true
	}
	normalized, err := json.Marshal(map[string]any{"proxies": source.Proxies})
	if err != nil {
		return nil, 0, errors.New("订阅包含不支持的节点字段")
	}
	return normalized, len(source.Proxies), nil
}
