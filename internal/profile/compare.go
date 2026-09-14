package profile

import "encoding/json"

// canonical 把一条出站的 JSON 规整成键序固定的紧凑形式。同样的内容,面板发来的、缓存里存过一遍的、
// 改过名字重新编码的,字节都可能不一样,比内容得先规整;规整不了(不是合法 JSON)就原样比。
func canonical(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v) // encoding/json 编码 map 时按键排序,顺序就固定了
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// nodeMap tag → 规整后的出站。
func (p *Profile) nodeMap() map[string]string {
	if p == nil {
		return map[string]string{}
	}
	m := make(map[string]string, len(p.Outbounds))
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) || p.Tags[i] == "" {
			continue
		}
		m[p.Tags[i]] = canonical(raw)
	}
	return m
}

// Node 按 tag 找一个节点,返回规整后的出站 JSON;没有这个节点返回 false。
func (p *Profile) Node(tag string) (string, bool) {
	v, ok := p.nodeMap()[tag]
	return v, ok
}

// Change 两份订阅之间节点的差别。
type Change struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"` // 同名但参数变了
}

// Empty 没有任何差别。
func (c Change) Empty() bool { return c.Added == 0 && c.Removed == 0 && c.Changed == 0 }

// Diff 从 a 到 b 节点的变化。只看内容不看顺序:顺序变了不影响连接,不值得为它重连。
// nil 当作空订阅。
func Diff(a, b *Profile) Change {
	am, bm := a.nodeMap(), b.nodeMap()
	var c Change
	for tag, old := range am {
		nw, ok := bm[tag]
		switch {
		case !ok:
			c.Removed++
		case nw != old:
			c.Changed++
		}
	}
	for tag := range bm {
		if _, ok := am[tag]; !ok {
			c.Added++
		}
	}
	return c
}

// Subset 只留这些 tag 的节点(下标对齐),别的字段照抄;找不到的 tag 略过。
func (p *Profile) Subset(tags []string) *Profile {
	if p == nil {
		return &Profile{}
	}
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	q := *p
	q.Outbounds, q.Tags = nil, nil
	for i, t := range p.Tags {
		if want[t] && i < len(p.Outbounds) {
			q.Outbounds = append(q.Outbounds, p.Outbounds[i])
			q.Tags = append(q.Tags, t)
		}
	}
	return &q
}

// Same 节点集合与各自参数完全一致(顺序不算)。
func Same(a, b *Profile) bool { return Diff(a, b).Empty() }
