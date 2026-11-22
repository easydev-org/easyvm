package net

// THE EGRESS ENVIRONMENT, IN ONE PLACE, BECAUSE THERE ARE NOW THREE VARIABLES THAT WORK TOGETHER AND
// EACH ONE ALONE IS A GUEST WITH PART OF ITS NETWORK.
//
//	NONE SET                    CI, AND THE DEFAULT. No proxy, no tun device, no fake addresses: the
//	                            guest reaches the internet directly, exactly as it would on a desk.
//	                            This is the case a runner gets and it must stay working.
//
//	EASYVM_TRANSPARENT_PROXY    the HTTP CONNECT proxy for TCP, host:port. Unset means TCP is not
//	                            proxied. See proxy.go.
//
//	EASYVM_UDP_PROXY            the SOCKS5 address for UDP, host:port. It is SEPARATE from the one above
//	                            on purpose: a proxy usually serves CONNECT and UDP ASSOCIATE on
//	                            different ports (mihomo: 7890 and 7891), and deriving one from the other
//	                            by adding one is a guess that is right for exactly one implementation.
//	                            Unset means no tun device is created at all. See udprelay.go.
//
//	EASYVM_EGRESS_RULES=1       exposes /api/egress. Without it that endpoint answers 403 naming this
//	                            variable. The RULES themselves do not need it - a rule set through the
//	                            API keeps working - but there is no way to set one without it.
//
//	EASYVM_FAKE_IP=1            the resolver INVENTS addresses instead of forwarding. See fakeip.go for
//	                            what that buys and what it costs. Without it the resolver forwards and
//	                            records, which is the safer default because a bake that inspects a DNS
//	                            answer would notice the difference.
//
// THE COMBINATION THAT IS FULLY USABLE is all four: proxy and udp proxy for the two transports, the
// rules API to name destinations, and fake-ip so a name survives to the connection even when the guest
// uses its own resolver. Everything short of that degrades rather than fails, which is the property
// this file exists to state: each variable removes a limitation, and none of them is required for a
// guest to boot.
