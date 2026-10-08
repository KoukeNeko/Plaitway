// Package fsperm keeps the daemon's files and directories private. On Unix
// that is the permission bits (0700 for a directory, 0600 for a file). On
// Windows the mode bits of a file are synthetic, so it is an access list that
// is protected from inheritance and names only SYSTEM, Administrators and, for
// a daemon that is not elevated, the user who runs it.
//
// On Windows, what already exists is not trusted either. %ProgramData% lets
// every user create a directory, and an owner can always give itself access
// again, so an object that is owned by anyone but SYSTEM, Administrators or
// the account of the process is refused instead of repaired. A junction or
// symbolic link is refused instead of followed, and so is a file that has
// another name, because the access list belongs to the file and not to a name.
// Restrict checks the whole tree below a directory, and every check and repair
// is made on an open handle. Entries are opened by the exact name the directory
// lists, since Windows drops a trailing dot or space from any other spelling,
// and whether a path is inside the data root is decided by where Windows
// resolves it, not by how it is written.
package fsperm
