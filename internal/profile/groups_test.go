package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 仿 m-ui 的 sing-box JSON:proxy 选择组、auto 全量测速组、按线路的测速组,再加几种第三方订阅里会出现的怪组。
const groupSample = `{"outbounds":[
  {"type":"selector","tag":"proxy","outbounds":["auto","香港极速1","hk-A","hk-B","jp-A","jp-B","proxy","ext"]},
  {"type":"urltest","tag":"auto","outbounds":["hk-A","hk-B","jp-A","jp-B","proxy","ext"],"url":"http://www.gstatic.com/generate_204","interval":"3m"},
  {"type":"urltest","tag":"香港极速1","outbounds":["hk-A","hk-B"],"url":"http://evil.example/track","interval":"10s","tolerance":1},
  {"type":"urltest","tag":" 日本 ","outbounds":["jp-A","jp-B","direct","不存在","jp-A"]},
  {"type":"urltest","tag":"单成员","outbounds":["hk-A","direct"]},
  {"type":"urltest","tag":"重复","outbounds":["hk-B","hk-A"]},
  {"type":"urltest","tag":"proxy","outbounds":["hk-A","jp-A"]},
  {"type":"urltest","tag":"自动选择","outbounds":["ext","proxy","jp-B","jp-A","hk-B","hk-A"]},
  {"type":"urltest","tag":"hk-A","outbounds":["hk-A","jp-B"]},
  {"type":"urltest","tag":"借名","outbounds":["proxy","hk-A"]},
  {"type":"urltest","tag":"组套组","outbounds":["香港极速1","jp-A"]},
  {"type":"urltest","tag":"怪成员","outbounds":"hk-A"},
  {"type":"hysteria2","tag":"hk-A","server":"1.1.1.1","server_port":443,"password":"p"},
  {"type":"hysteria2","tag":"hk-B","server":"2.2.2.2","server_port":443,"password":"p"},
  {"type":"anytls","tag":"jp-A","server":"3.3.3.3","server_port":443,"password":"p"},
  {"type":"anytls","tag":"jp-B","server":"4.4.4.4","server_port":443,"password":"p"},
  {"type":"trojan","tag":"proxy","server":"5.5.5.5","server_port":443,"password":"p"},
  {"type":"trojan","tag":"ext","server":"6.6.6.6","server_port":443,"password":"p"},
  {"type":"direct","tag":"direct"}
]}`

// 订阅里的线路组:只认 urltest;成员换成清洗后的节点名,对不上的丢掉;不到两个成员、成员是全部节点、保留名、
// 和前面重复的组都不要;组名去空白、和节点撞名加序号;订阅给的测速地址与间隔一概不留。节点本身照旧。
func TestParseLineGroups(t *testing.T) {
	p, err := Parse([]byte(groupSample), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"hk-A", "hk-B", "jp-A", "jp-B", "proxy 2", "ext"}; !reflect.DeepEqual(p.Tags, want) {
		t.Fatalf("节点不该受线路组影响: %v", p.Tags)
	}
	want := []Group{
		{Tag: "香港极速1", Members: []string{"hk-A", "hk-B"}},
		{Tag: "日本", Members: []string{"jp-A", "jp-B"}},
		{Tag: "hk-A 2", Members: []string{"hk-A", "jp-B"}},
		{Tag: "借名", Members: []string{"proxy 2", "hk-A"}},
	}
	if !reflect.DeepEqual(p.Groups, want) {
		t.Fatalf("线路组不对:\n得 %+v\n应 %+v", p.Groups, want)
	}
	if !reflect.DeepEqual(p.Whole, []string{"自动选择"}) {
		t.Fatalf("成员是全部节点的组不当线路组,只记下名字(auto 是保留名,不记): %v", p.Whole)
	}
	b, _ := json.Marshal(p)
	if s := string(b); strings.Contains(s, "evil.example") || strings.Contains(s, "interval") || strings.Contains(s, "tolerance") {
		t.Fatalf("订阅给的测速地址、间隔不能存下来: %s", s)
	}
	if p.Parser != parserVersion {
		t.Fatalf("新解析的缓存要记上解析器版本: %d", p.Parser)
	}
	if g, ok := p.Group("日本"); !ok || len(g.Members) != 2 {
		t.Fatalf("按名字找线路组: %+v %v", g, ok)
	}
	if _, ok := p.Group("单成员"); ok {
		t.Fatal("被丢掉的组不该找得到")
	}
	if p.GroupOf("jp-B") != "日本" || p.GroupOf("hk-A") != "香港极速1" || p.GroupOf("ext") != "" {
		t.Fatalf("节点所在的线路组(第一个包含它的): %q %q %q", p.GroupOf("jp-B"), p.GroupOf("hk-A"), p.GroupOf("ext"))
	}
	var nilP *Profile
	if _, ok := nilP.Group("日本"); ok || nilP.GroupOf("hk-A") != "" {
		t.Fatal("nil 订阅里什么都没有")
	}
}

// 没有线路组的订阅(老面板、单服务器):Groups 是空的,缓存里也不多出这一项。
func TestParseNoGroups(t *testing.T) {
	p, err := Parse([]byte(sample), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 0 {
		t.Fatalf("老格式的订阅不该有线路组(auto 是保留名、又是全部节点): %+v", p.Groups)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), `"groups"`) {
		t.Fatalf("没有线路组时缓存里不写这一项: %s", b)
	}
}

// 剔掉建不起来的节点时线路组跟着剪:不在子集里的成员去掉,剪空的组丢掉,只剩一个成员的照留;原订阅不动。
func TestSubsetPrunesGroups(t *testing.T) {
	p, err := Parse([]byte(groupSample), nil)
	if err != nil {
		t.Fatal(err)
	}
	q := p.Subset([]string{"hk-A", "jp-A", "jp-B"})
	want := []Group{
		{Tag: "香港极速1", Members: []string{"hk-A"}},
		{Tag: "日本", Members: []string{"jp-A", "jp-B"}},
		{Tag: "hk-A 2", Members: []string{"hk-A", "jp-B"}},
		{Tag: "借名", Members: []string{"hk-A"}},
	}
	if !reflect.DeepEqual(q.Groups, want) {
		t.Fatalf("子集里的线路组:\n得 %+v\n应 %+v", q.Groups, want)
	}
	if q = p.Subset([]string{"ext"}); len(q.Groups) != 0 {
		t.Fatalf("成员全被剔掉的组要丢掉: %+v", q.Groups)
	}
	if len(p.Groups) != 4 || len(p.Groups[0].Members) != 2 {
		t.Fatalf("Subset 不能改原订阅: %+v", p.Groups)
	}
}

// 旧版本存的缓存(没有 groups、没有 parser)照样读得出来,只是算陈旧(Outdated):升级后连上就补拉一次,线路组不用等满
// 刷新间隔;但不算过期(Stale),连上之前不为它直连拉订阅。新缓存来回读写一致;手动节点两样都不算。
func TestCacheGroupsCompat(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	body := `{"url":"https://p.example/sub/x","title":"t","fetchedAt":` + itoa64(time.Now().Unix()) + `,
	  "outbounds":[{"type":"anytls","tag":"a","server":"1.1.1.1","server_port":443}],"tags":["a"],"usage":{}}`
	if err := os.WriteFile(old, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(old)
	if err != nil {
		t.Fatalf("旧缓存要读得出来: %v", err)
	}
	if len(p.Groups) != 0 || p.Parser != 0 {
		t.Fatalf("旧缓存没有线路组: %+v", p)
	}
	if !p.Outdated() || p.Stale(24*time.Hour) {
		t.Fatal("旧版解析器存的缓存刚拉过:算陈旧(连上后补拉),不算过期(连上之前不直连拉)")
	}
	p.Manual = true
	if p.Stale(0) || p.Outdated() {
		t.Fatal("手动节点没有地方可拉,永远不过期、不陈旧")
	}

	fresh, err := Parse([]byte(groupSample), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "new.json")
	if err := fresh.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Groups, fresh.Groups) || !reflect.DeepEqual(back.Whole, fresh.Whole) || back.Parser != parserVersion {
		t.Fatalf("线路组来回读写不一致: %+v %v / %d", back.Groups, back.Whole, back.Parser)
	}
	if back.Stale(24*time.Hour) || back.Outdated() {
		t.Fatal("新版刚拉的缓存不该算过期或陈旧")
	}
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// 订阅里只剩一条线路(m-ui 照样给它出组,可成员就是全部节点):不当线路组(它和自动选择是一回事,界面不单列),
// 只把名字记进 Whole —— 选着这条线路的人,刷新后还认得出"线路还在,只是只剩它了"(见 builder.Effective)。
// 起名在正常的线路组之后,和节点、保留名撞了照样加序号;保留名、不到两个成员的不记;和节点同名的按节点算。
func TestParseWholeGroups(t *testing.T) {
	body := `{"outbounds":[
	  {"type":"urltest","tag":"auto","outbounds":["hk-A","hk-B"]},
	  {"type":"urltest","tag":"香港1","outbounds":["hk-A","hk-B"]},
	  {"type":"urltest","tag":"hk-A","outbounds":["hk-B","hk-A","direct"]},
	  {"type":"urltest","tag":"单成员","outbounds":["hk-A"]},
	  {"type":"hysteria2","tag":"hk-A","server":"1.1.1.1","server_port":443,"password":"p"},
	  {"type":"hysteria2","tag":"hk-B","server":"2.2.2.2","server_port":443,"password":"p"}
	]}`
	p, err := Parse([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 0 || !reflect.DeepEqual(p.Whole, []string{"香港1", "hk-A 2"}) {
		t.Fatalf("成员是全部节点的组只记名字: groups=%+v whole=%v", p.Groups, p.Whole)
	}
	if _, ok := p.Group("香港1"); ok {
		t.Fatal("它不是线路组(界面不单列、配置里不放)")
	}
	g, ok := p.WholeGroup("香港1")
	if !ok || !reflect.DeepEqual(g.Members, p.Tags) {
		t.Fatalf("成员就是全部节点: %+v %v", g, ok)
	}
	for _, tag := range []string{"hk-A", "auto", "单成员", "", "不存在"} {
		if _, ok := p.WholeGroup(tag); ok {
			t.Fatalf("%q 不是成员为全部节点的组(节点、保留名、不到两个成员的、没有的)", tag)
		}
	}
	// 剔掉建不起来的节点时照抄(成员就是子集的全部节点)
	if q := p.Subset([]string{"hk-A"}); !reflect.DeepEqual(q.Whole, p.Whole) {
		t.Fatalf("子集里照抄: %v", q.Whole)
	}
	var nilP *Profile
	if _, ok := nilP.WholeGroup("香港1"); ok {
		t.Fatal("nil 订阅里什么都没有")
	}
	// 正常的线路组起名不受它影响:同名的正常组照旧叫原名
	p, err = Parse([]byte(`{"outbounds":[
	  {"type":"urltest","tag":"组","outbounds":["a","b","c"]},
	  {"type":"urltest","tag":"组","outbounds":["a","b"]},
	  {"type":"socks","tag":"a","server":"1.1.1.1","server_port":1},
	  {"type":"socks","tag":"b","server":"1.1.1.2","server_port":1},
	  {"type":"socks","tag":"c","server":"1.1.1.3","server_port":1}
	]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 1 || p.Groups[0].Tag != "组" || !reflect.DeepEqual(p.Whole, []string{"组 2"}) {
		t.Fatalf("正常的线路组先起名: groups=%+v whole=%v", p.Groups, p.Whole)
	}
}
