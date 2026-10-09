package linux

// CheckResolved says why DNS settings cannot be written on this host right now:
// resolvectl is not installed, or systemd-resolved does not answer. It is nil
// when resolved answers. It only reads, and needs no privilege: it asks the
// question the first Apply asks, so that a daemon can say so at start rather
// than when the first tunnel comes up.
func CheckResolved(opts DNSOptions) error {
	if opts.Run == nil {
		opts.Run = runDNSCommand
	}
	d := newDNSConfigurator(opts)
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkResolved()
}
