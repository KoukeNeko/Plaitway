package ovpn

import "errors"

// errNoExitSignal says the OS has no way to ask a process to exit that the
// management interface does not offer; the caller has to stop it by force.
var errNoExitSignal = errors.New("this OS cannot ask a process to exit")
