// Package clash 内核 Clash API 的客户端(只连 127.0.0.1,带密钥):实时速度、节点延迟、连接列表。
// 托盘客户端用它;服务自己不用。
package clash

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	base   string
	secret string
	http   *http.Client
}

func New(port int, secret string) *Client {
	return &Client{base: "http://127.0.0.1:" + strconv.Itoa(port), secret: secret, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return fmt.Errorf("clash api: %s", e.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Proxy 一个出站(节点或分组)。
type Proxy struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Now     string   `json:"now,omitempty"`
	All     []string `json:"all,omitempty"`
	History []struct {
		Time  string `json:"time"`
		Delay int    `json:"delay"`
	} `json:"history"`
}

// LastDelay 最近一次测得的延迟(0 = 没测过或失败)。
func (p Proxy) LastDelay() int {
	if len(p.History) == 0 {
		return 0
	}
	return p.History[len(p.History)-1].Delay
}

func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var out struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	if err := c.do(ctx, http.MethodGet, "/proxies", &out); err != nil {
		return nil, err
	}
	return out.Proxies, nil
}

// GroupDelay 给分组里所有节点测一次延迟,返回 节点 → 毫秒(失败的不在结果里)。
func (c *Client) GroupDelay(ctx context.Context, group, testURL string, timeout time.Duration) (map[string]int, error) {
	q := url.Values{"url": {testURL}, "timeout": {strconv.Itoa(int(timeout.Milliseconds()))}}
	out := map[string]int{}
	err := c.do(ctx, http.MethodGet, "/group/"+url.PathEscape(group)+"/delay?"+q.Encode(), &out)
	return out, err
}

// Delay 单个节点测延迟。
func (c *Client) Delay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error) {
	q := url.Values{"url": {testURL}, "timeout": {strconv.Itoa(int(timeout.Milliseconds()))}}
	var out struct {
		Delay int `json:"delay"`
	}
	err := c.do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(name)+"/delay?"+q.Encode(), &out)
	return out.Delay, err
}

// Traffic 订阅实时速度:内核每秒推一行 {"up":字节/秒,"down":字节/秒},直到 ctx 结束或连接断开。
func (c *Client) Traffic(ctx context.Context, fn func(up, down int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/traffic", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	resp, err := (&http.Client{}).Do(req) // 流式,不能带整体超时
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clash api: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var t struct {
			Up   int64 `json:"up"`
			Down int64 `json:"down"`
		}
		if json.Unmarshal(sc.Bytes(), &t) == nil {
			fn(t.Up, t.Down)
		}
	}
	return sc.Err()
}

// Conn 一条活动连接。
type Conn struct {
	ID       string `json:"id"`
	Metadata struct {
		Network         string `json:"network"`
		Host            string `json:"host"`
		DestinationIP   string `json:"destinationIP"`
		DestinationPort string `json:"destinationPort"`
		SourceIP        string `json:"sourceIP"`
		ProcessPath     string `json:"processPath"`
	} `json:"metadata"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Start    string   `json:"start"`
	Chains   []string `json:"chains"`
	Rule     string   `json:"rule"`
}

type Connections struct {
	DownloadTotal int64  `json:"downloadTotal"`
	UploadTotal   int64  `json:"uploadTotal"`
	Connections   []Conn `json:"connections"`
}

func (c *Client) Connections(ctx context.Context) (*Connections, error) {
	var out Connections
	if err := c.do(ctx, http.MethodGet, "/connections", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CloseConnection(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/connections/"+url.PathEscape(id), nil)
}
