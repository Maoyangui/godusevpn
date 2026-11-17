package ipc

// NodeList 内核在跑时节点列表列哪些、按什么顺序:按订阅缓存(StateView.Nodes,首项 auto),kernel 是内核 proxy 组里的
// 全部名字。内核里有的照用内核的信息;没有的也列出来 —— 刷新新加进来的(选它会重建配置重连用上)、内核建不起来的
// (StateView.Unsupported)。以前只列内核里的:订阅新加的节点要等重连才看得见,刷新日志却写着"已更新到节点列表"。
// 订阅里已经删掉、内核里还留着的不列。缓存是空的(还没拉到过)就按内核的列。
func NodeList(view, kernel []string) (names []string, inKernel map[string]bool) {
	inKernel = make(map[string]bool, len(kernel))
	for _, n := range kernel {
		inKernel[n] = true
	}
	if len(view) == 0 {
		return kernel, inKernel
	}
	return view, inKernel
}

// NodeType 节点列表上的协议标签:以订阅缓存里的为准(StateView.Types);缓存里没有(老版本的服务不带这一项)才用内核报的。
// 只认内核的话,面板把线路从 hysteria2 换成 vless、客户端刷新之后,列表上还一直写着 hysteria2 —— 直到重连。
func (v StateView) NodeType(name, kernel string) string {
	if t := v.Types[name]; t != "" {
		return t
	}
	return kernel
}

// NodeChanged 这个节点的参数刷新后变了、内核里还是旧的那份(见 StateView.Changed)。
func (v StateView) NodeChanged(name string) bool {
	for _, n := range v.Changed {
		if n == name {
			return true
		}
	}
	return false
}
