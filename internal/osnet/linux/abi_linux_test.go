package linux

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The wire constants of rtmsg.go are written out because the portable files
// cannot import the Linux-only parts of x/sys. They must be what the kernel's
// headers say.
func TestWireConstantsMatchTheKernelHeaders(t *testing.T) {
	tests := []struct {
		name      string
		got, want int
	}{
		{"nlmsgHdrLen", nlmsgHdrLen, unix.NLMSG_HDRLEN},
		{"nlmsgError", nlmsgError, unix.NLMSG_ERROR},
		{"nlmsgDone", nlmsgDone, unix.NLMSG_DONE},
		{"nlmFRequest", nlmFRequest, unix.NLM_F_REQUEST},
		{"nlmFAck", nlmFAck, unix.NLM_F_ACK},
		{"nlmFDump", nlmFDump, unix.NLM_F_DUMP},
		{"nlmFExcl", nlmFExcl, unix.NLM_F_EXCL},
		{"nlmFCreate", nlmFCreate, unix.NLM_F_CREATE},
		{"nlmFDumpIntr", nlmFDumpIntr, unix.NLM_F_DUMP_INTR},
		{"nlmFCapped", nlmFCapped, unix.NLM_F_CAPPED},
		{"nlmFAckTLVs", nlmFAckTLVs, unix.NLM_F_ACK_TLVS},
		{"rtmNewLink", rtmNewLink, unix.RTM_NEWLINK},
		{"rtmDelLink", rtmDelLink, unix.RTM_DELLINK},
		{"rtmGetLink", rtmGetLink, unix.RTM_GETLINK},
		{"rtmNewAddr", rtmNewAddr, unix.RTM_NEWADDR},
		{"rtmDelAddr", rtmDelAddr, unix.RTM_DELADDR},
		{"rtmGetAddr", rtmGetAddr, unix.RTM_GETADDR},
		{"rtmNewRoute", rtmNewRoute, unix.RTM_NEWROUTE},
		{"rtmDelRoute", rtmDelRoute, unix.RTM_DELROUTE},
		{"rtmGetRoute", rtmGetRoute, unix.RTM_GETROUTE},
		{"rtmgrpLink", rtmgrpLink, unix.RTMGRP_LINK},
		{"rtmgrpIPv4Addr", rtmgrpIPv4Addr, unix.RTMGRP_IPV4_IFADDR},
		{"rtmgrpIPv4Route", rtmgrpIPv4Route, unix.RTMGRP_IPV4_ROUTE},
		{"rtmgrpIPv6Addr", rtmgrpIPv6Addr, unix.RTMGRP_IPV6_IFADDR},
		{"rtmgrpIPv6Route", rtmgrpIPv6Route, unix.RTMGRP_IPV6_ROUTE},
		{"afUnspec", afUnspec, unix.AF_UNSPEC},
		{"afInet", afInet, unix.AF_INET},
		{"afInet6", afInet6, unix.AF_INET6},
		{"nlaTypeMask", nlaTypeMask, ^(unix.NLA_F_NESTED | unix.NLA_F_NET_BYTEORDER) & 0xffff},
		{"nlmsgErrAttrMsg", nlmsgErrAttrMsg, unix.NLMSGERR_ATTR_MSG},
		{"rtMsgLen", rtMsgLen, unix.SizeofRtMsg},
		{"ifInfoMsgLen", ifInfoMsgLen, unix.SizeofIfInfomsg},
		{"ifAddrMsgLen", ifAddrMsgLen, unix.SizeofIfAddrmsg},
		{"rtNextHopLen", rtNextHopLen, unix.SizeofRtNexthop},
		{"rtaDst", rtaDst, unix.RTA_DST},
		{"rtaOif", rtaOif, unix.RTA_OIF},
		{"rtaGateway", rtaGateway, unix.RTA_GATEWAY},
		{"rtaPriority", rtaPriority, unix.RTA_PRIORITY},
		{"rtaMultipath", rtaMultipath, unix.RTA_MULTIPATH},
		{"rtaTable", rtaTable, unix.RTA_TABLE},
		{"rtaVia", rtaVia, unix.RTA_VIA},
		{"rtnUnicast", rtnUnicast, unix.RTN_UNICAST},
		{"rtnBlackhole", rtnBlackhole, unix.RTN_BLACKHOLE},
		{"rtnUnreachable", rtnUnreachable, unix.RTN_UNREACHABLE},
		{"rtnProhibit", rtnProhibit, unix.RTN_PROHIBIT},
		{"rtprotRedirect", rtprotRedirect, unix.RTPROT_REDIRECT},
		{"rtprotKernel", rtprotKernel, unix.RTPROT_KERNEL},
		{"rtprotRA", rtprotRA, unix.RTPROT_RA},
		{"rtScopeUniverse", rtScopeUniverse, unix.RT_SCOPE_UNIVERSE},
		{"rtScopeLink", rtScopeLink, unix.RT_SCOPE_LINK},
		{"rtScopeNowhere", rtScopeNowhere, unix.RT_SCOPE_NOWHERE},
		{"rtTableMain", rtTableMain, unix.RT_TABLE_MAIN},
		{"rtmFCloned", rtmFCloned, unix.RTM_F_CLONED},
		{"iflaIfName", iflaIfName, unix.IFLA_IFNAME},
		{"iflaLinkInfo", iflaLinkInfo, unix.IFLA_LINKINFO},
		{"iflaInfoKind", iflaInfoKind, unix.IFLA_INFO_KIND},
		{"iflaMTU", iflaMTU, unix.IFLA_MTU},
		{"iffUp", iffUp, unix.IFF_UP},
		{"iffLoopback", iffLoopback, unix.IFF_LOOPBACK},
		{"iffLowerUp", iffLowerUp, unix.IFF_LOWER_UP},
		{"ifaAddress", ifaAddress, unix.IFA_ADDRESS},
		{"ifaLocal", ifaLocal, unix.IFA_LOCAL},
		{"ifaBroadcast", ifaBroadcast, unix.IFA_BROADCAST},
		{"arphrdPPP", arphrdPPP, unix.ARPHRD_PPP},
		{"arphrdTunnel", arphrdTunnel, unix.ARPHRD_TUNNEL},
		{"arphrdTunnel6", arphrdTunnel6, unix.ARPHRD_TUNNEL6},
		{"arphrdLoopback", arphrdLoopback, unix.ARPHRD_LOOPBACK},
		{"arphrdSIT", arphrdSIT, unix.ARPHRD_SIT},
		{"arphrdIPGRE", arphrdIPGRE, unix.ARPHRD_IPGRE},
		{"arphrdNone", arphrdNone, unix.ARPHRD_NONE},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, the kernel's is %d", tt.name, tt.got, tt.want)
		}
	}
}
