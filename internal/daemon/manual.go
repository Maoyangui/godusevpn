package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/nodelink"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 手动节点:不是从订阅拉来的,用户自己填或粘贴分享链接。放在一条特殊的订阅里(settings.Profile.Manual),
// 这样节点列表、切换、测速、规则这些全都照旧走,不用为它单开一条路。缓存文件就是它的存储,和订阅一样只给 root 读。

func (d *Daemon) registerManualHandlers(h func(string, ipc.Handler)) {
	h(ipc.MGetManualNodes, func(json.RawMessage) (any, error) { return d.manualView(), nil })
	h(ipc.MParseNodeLinks, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Text string `json:"text"`
		}](p)
		if err != nil {
			return nil, err
		}
		nodes, errs := nodelink.ParseAll(in.Text)
		return ipc.ParsedNodes{Nodes: nodes, Errors: errs}, nil
	})
	h(ipc.MSaveManualNodes, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Nodes   []map[string]any `json:"nodes"`
			Replace string           `json:"replace"`
			Name    string           `json:"name"`
		}](p)
		if err != nil {
			return nil, err
		}
		return d.saveManualNodes(in.Nodes, in.Replace, in.Name)
	})
	h(ipc.MRemoveManualNode, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Tag string `json:"tag"`
		}](p)
		if err != nil {
			return nil, err
		}
		return d.removeManualNode(in.Tag)
	})
}

// manualProfile 手动节点那条订阅和它的节点;还没有时返回 nil。
func (d *Daemon) manualProfile() (*settings.Profile, *profile.Profile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.settings.Profiles {
		if sp := &d.settings.Profiles[i]; sp.Manual {
			cp := *sp
			return &cp, d.profiles[sp.ID]
		}
	}
	return nil, nil
}

func (d *Daemon) manualView() ipc.ManualNodes {
	view := ipc.ManualNodes{Nodes: []json.RawMessage{}}
	sp, p := d.manualProfile()
	if sp == nil {
		return view
	}
	view.ID = sp.ID
	if p != nil {
		view.Nodes = p.Outbounds
	}
	return view
}

// buildManual 把节点列表变成订阅缓存:标签撞名、和内部标签撞名都由 profile.Parse 处理。
func buildManual(outs []json.RawMessage) (*profile.Profile, error) {
	body, err := json.Marshal(map[string]any{"outbounds": outs})
	if err != nil {
		return nil, err
	}
	p, err := profile.Parse(body, nil)
	if err != nil {
		return nil, err
	}
	p.Manual = true
	return p, nil
}

// nodeErr 节点内容的错误带上错误码,界面按码翻译(Msg 是中文,给命令行)。
func nodeErr(err error) error {
	if code := nodelink.CodeOf(err); code != "" {
		return &ipc.CallError{Code: "E_NODE_" + strings.ToUpper(code), Msg: err.Error()}
	}
	return err
}

// nodeKey 除名字以外的内容,用来认出重复的节点。map 序列化时键是排好序的。
func nodeKey(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	delete(m, "tag")
	b, _ := json.Marshal(m)
	return string(b)
}

// freeTag 给编辑后的节点找个没被占的名字:撞名时让它让开,别让没动过的节点被改名(选着它的就对不上了)。
func freeTag(tag string, others []string) string {
	used := map[string]bool{}
	for _, t := range others {
		used[t] = true
	}
	for i, name := 2, tag; ; i++ {
		if !used[name] {
			return name
		}
		name = fmt.Sprintf("%s %d", tag, i)
	}
}

// saveManualNodes 添加节点;replace 不为空时替换这个标签的节点(编辑)。保存前内核校验这次动过的节点,填得不对的不落盘。
func (d *Daemon) saveManualNodes(nodes []map[string]any, replace, name string) ([]ipc.ProfileView, error) {
	if len(nodes) == 0 {
		return nil, &ipc.CallError{Code: "E_NODE_EMPTY", Msg: "没有要保存的节点"}
	}
	if replace != "" && len(nodes) != 1 {
		return nil, errors.New("编辑时只能保存一个节点")
	}
	d.manualMu.Lock()
	defer d.manualMu.Unlock()

	sp, cur := d.manualProfile()
	var outs []json.RawMessage
	var others []string
	at := -1 // 被替换的节点的位置
	if cur != nil {
		outs = append(outs, cur.Outbounds...)
		for j, t := range cur.Tags {
			if replace != "" && t == replace {
				at = j
			} else {
				others = append(others, t)
			}
		}
	}
	if replace != "" && at < 0 {
		return nil, &ipc.CallError{Code: "E_NODE_GONE", Msg: "没有这个节点"}
	}
	fresh := make([]json.RawMessage, 0, len(nodes))
	seen := map[string]bool{} // 添加时跳过内容完全相同的节点:同一批链接再贴一次不该翻倍
	if replace == "" {
		for _, o := range outs {
			seen[nodeKey(o)] = true
		}
	}
	for _, n := range nodes {
		clean, err := nodelink.Normalize(n)
		if err != nil {
			return nil, nodeErr(err)
		}
		if replace != "" {
			clean["tag"] = freeTag(clean["tag"].(string), others)
		}
		b, err := json.Marshal(clean)
		if err != nil {
			return nil, err
		}
		if replace == "" {
			if key := nodeKey(b); seen[key] {
				continue
			} else {
				seen[key] = true
			}
		}
		fresh = append(fresh, b)
	}
	if len(fresh) == 0 {
		return nil, &ipc.CallError{Code: "E_NODE_DUP", Msg: "这个节点已经添加过了"}
	}
	if replace != "" {
		outs[at] = fresh[0]
	} else {
		outs = append(outs, fresh...)
	}
	np, err := buildManual(outs)
	if err != nil {
		return nil, err
	}
	// 只查这次动过的:旧节点就算因为升级建不起来了,也不该挡住添加新节点
	first0, changed := len(outs)-len(fresh), len(fresh)
	if replace != "" {
		first0, changed = at, 1
	}
	if bad := d.core.Unbuildable(np.Outbounds[first0 : first0+changed]); len(bad) > 0 {
		for tag, why := range bad {
			return nil, &ipc.CallError{Code: "E_NODE_INVALID", Msg: fmt.Sprintf("「%s」:%s", tag, why)}
		}
	}

	s := d.getSettings()
	first := false
	if sp == nil {
		name = strings.TrimSpace(name)
		if name == "" {
			name = "手动节点"
		}
		s.Profiles = append(s.Profiles, settings.Profile{ID: settings.NewID(), Name: name, Manual: true})
		sp = &s.Profiles[len(s.Profiles)-1]
		if len(s.Profiles) == 1 {
			s.ActiveProfile, first = sp.ID, true
		}
	}
	id := sp.ID
	// 编辑时改了名字:手动选着这个节点的话跟过去,不然保存后"当前节点"就对不上了
	if replace != "" && s.ActiveProfile == id && s.Selected == replace {
		s.Selected = np.Tags[at]
	}
	if err := d.setSettings(s); err != nil {
		return nil, err
	}
	d.setProfileCache(id, np)
	if first {
		_ = d.restart() // 还没连着,只是把想连的状态接上;和添加第一条订阅一样
	} else {
		d.afterRefresh(id)
	}
	views, _ := d.profileViews()
	return views, nil
}

// removeManualNode 删一个节点。删到一个不剩就把这条订阅也删了:没有节点的订阅用不了,留着只是摆设。
func (d *Daemon) removeManualNode(tag string) ([]ipc.ProfileView, error) {
	d.manualMu.Lock()
	defer d.manualMu.Unlock()
	sp, cur := d.manualProfile()
	if sp == nil || cur == nil {
		return nil, &ipc.CallError{Code: "E_NODE_GONE", Msg: "没有这个节点"}
	}
	var keep []json.RawMessage
	found := false
	for j, t := range cur.Tags {
		if t == tag {
			found = true
			continue
		}
		keep = append(keep, cur.Outbounds[j])
	}
	if !found {
		return nil, &ipc.CallError{Code: "E_NODE_GONE", Msg: "没有这个节点"}
	}
	if len(keep) == 0 {
		raw, _ := json.Marshal(map[string]string{"id": sp.ID})
		if _, err := d.Dispatch(ipc.MRemoveProfile, raw); err != nil {
			return nil, err
		}
		views, _ := d.profileViews()
		return views, nil
	}
	np, err := buildManual(keep)
	if err != nil {
		return nil, err
	}
	s := d.getSettings()
	if s.ActiveProfile == sp.ID && s.Selected == tag {
		s.Selected = "" // 正用着的节点被删了:回到自动选择
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
	}
	d.setProfileCache(sp.ID, np)
	d.afterRefresh(sp.ID)
	views, _ := d.profileViews()
	return views, nil
}
