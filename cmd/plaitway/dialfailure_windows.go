package main

import (
	"strconv"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// dialFailure picks the reason out of the message gRPC gives for a pipe it
// could not connect to: connection error: desc = "transport: Error while
// dialing: open \\.\pipe\plaitway: The system cannot find the file specified.".
// The reason is the text after the last colon, which Windows words itself; a
// refusal of the server (transport.RefusedServerPrefix) is kept whole.
func dialFailure(msg string) (reason string, ok bool) {
	const marker = "Error while dialing: "
	text := unquoteDescription(msg)
	i := strings.LastIndex(text, marker)
	if i < 0 {
		return "", false
	}
	reason = strings.TrimSuffix(text[i+len(marker):], `"`)
	if strings.HasPrefix(reason, transport.RefusedServerPrefix) {
		return reason, true
	}
	if colon := strings.LastIndex(reason, ": "); colon >= 0 {
		reason = reason[colon+len(": "):]
	}
	return reason, true
}

// unquoteDescription undoes the quoting gRPC applies to the description of a
// connection error, which doubles every backslash of a pipe name or account.
func unquoteDescription(msg string) string {
	const marker = "desc = "
	i := strings.Index(msg, marker)
	if i < 0 {
		return msg
	}
	text, err := strconv.Unquote(msg[i+len(marker):])
	if err != nil {
		return msg
	}
	return text
}

// refusal is what a client says when the server of the pipe is not one it
// trusts or could not be verified.
func refusal(reason string) (detail string, ok bool) {
	return strings.CutPrefix(reason, transport.RefusedServerPrefix)
}

// classifyDialFailure has no nobodyListens: a pipe whose server is gone does
// not exist any more, so it is a pipe that is missing.
func classifyDialFailure(reason string) dialProblem {
	switch reason {
	case windows.ERROR_FILE_NOT_FOUND.Error(), windows.ERROR_PATH_NOT_FOUND.Error():
		return socketMissing
	case windows.ERROR_ACCESS_DENIED.Error():
		return accessDenied
	}
	return otherProblem
}
