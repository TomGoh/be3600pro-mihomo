//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

type result struct {
	Name string `json:"name"`
	OK bool `json:"ok"`
	Milliseconds int64 `json:"milliseconds"`
	Error string `json:"error,omitempty"`
}

func main(){
	ifaceName:=flag.String("interface","en0","Mac interface that must carry test traffic")
	dns:=flag.String("dns","192.168.31.1:1053","DNS transport destination")
	flag.Parse()
	iface,err:=net.InterfaceByName(*ifaceName);if err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(2)}
	control:=func(network,address string,connection syscall.RawConn)error{
		var socketErr error
		err:=connection.Control(func(fd uintptr){
			if strings.HasSuffix(network,"6"){socketErr=syscall.SetsockoptInt(int(fd),syscall.IPPROTO_IPV6,syscall.IPV6_BOUND_IF,iface.Index)}else{socketErr=syscall.SetsockoptInt(int(fd),syscall.IPPROTO_IP,syscall.IP_BOUND_IF,iface.Index)}
		})
		if err!=nil{return err};return socketErr
	}
	dnsDialer:=net.Dialer{Timeout:8*time.Second,Control:control}
	resolver:=&net.Resolver{PreferGo:true,Dial:func(ctx context.Context,network,address string)(net.Conn,error){return dnsDialer.DialContext(ctx,"udp4",*dns)}}
	dialer:=net.Dialer{Timeout:8*time.Second,Control:control,Resolver:resolver}
	results:=[]result{}
	run:=func(name string,test func(context.Context)error){
		ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
		started:=time.Now();err:=test(ctx);r:=result{Name:name,OK:err==nil,Milliseconds:time.Since(started).Milliseconds()}
		if err!=nil{r.Error=err.Error()};results=append(results,r)
	}
	run("DNS A",func(ctx context.Context)error{ips,err:=resolver.LookupIP(ctx,"ip4","www.google.com");if err!=nil{return err};if len(ips)==0{return fmt.Errorf("DNS returned no A records")};return nil})
	for _,family:=range []string{"tcp4","tcp6"}{
		family:=family
		run("HTTPS "+family,func(ctx context.Context)error{
			transport:=&http.Transport{Proxy:nil,DialContext:func(ctx context.Context,network,address string)(net.Conn,error){return dialer.DialContext(ctx,family,address)}}
			defer transport.CloseIdleConnections()
			client:=&http.Client{Transport:transport,Timeout:9*time.Second}
			req,_:=http.NewRequestWithContext(ctx,"GET","https://www.google.com/generate_204",nil)
			response,err:=client.Do(req);if err!=nil{return err};defer response.Body.Close();_,_ = io.Copy(io.Discard,response.Body)
			if response.StatusCode!=204{return fmt.Errorf("HTTP status %d",response.StatusCode)};return nil
		})
	}
	for _,family:=range []string{"udp4","udp6"}{
		family:=family
		run("STUN "+family,func(ctx context.Context)error{
			ipKind:="ip4";if family=="udp6"{ipKind="ip6"}
			ips,err:=resolver.LookupIP(ctx,ipKind,"stun.l.google.com");if err!=nil{return err};if len(ips)==0{return fmt.Errorf("STUN hostname has no %s address",ipKind)}
			connection,err:=dialer.DialContext(ctx,family,net.JoinHostPort(ips[0].String(),"19302"));if err!=nil{return err};defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(8*time.Second))
			request:=make([]byte,20);binary.BigEndian.PutUint16(request,1);binary.BigEndian.PutUint32(request[4:],0x2112a442);_,_ = rand.Read(request[8:])
			if _,err=connection.Write(request);err!=nil{return err};reply:=make([]byte,2048);n,err:=connection.Read(reply);if err!=nil{return err}
			if n<20||binary.BigEndian.Uint16(reply)!=0x0101||string(reply[4:20])!=string(request[4:20]){return fmt.Errorf("invalid STUN binding response")};return nil
		})
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"interface":*ifaceName,"dns":*dns,"results":results})
	for _,r:=range results{if !r.OK{os.Exit(1)}}
}
