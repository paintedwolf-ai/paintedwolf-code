package egressproxy

// SocksAddr returns the SOCKS5 listener address.
func (b *Broker) SocksAddr() string {
	if b == nil || b.socksLn == nil {
		return ""
	}
	return b.socksLn.Addr().String()
}
