package linux

// listLinks reads every interface with its addresses.
func listLinks() ([]link, error) {
	conn, err := openNetlink(0)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.links(true)
}

// listLinkIdentities reads every interface without its addresses, which is all
// the event reader needs to classify a message.
func listLinkIdentities() ([]link, error) {
	conn, err := openNetlink(0)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.links(false)
}
