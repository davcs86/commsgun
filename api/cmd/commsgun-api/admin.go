package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/password"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	exitDone    = 0
	exitRefused = 1
	exitUsage   = 2
	adminUsage  = "usage: commsgun-api admin create|add|reset-password|disable|enable|unlink <username> (password on stdin)\n"
)

var takesPassword = map[string]bool{"create": true, "add": true, "reset-password": true}

var adminVerbs = map[string]bool{"create": true, "add": true, "reset-password": true, "disable": true, "enable": true, "unlink": true}

// runAdmin reads only DATA_DIR and opens no socket; output never carries the password or the username.
func runAdmin(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer, clk clock.Clock, h *password.Hasher) int {
	if len(args) != 2 || !adminVerbs[args[0]] {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	verb, username := args[0], args[1]
	if !store.ValidUsername(username) {
		return refuse(stderr, fmt.Sprintf("invalid username: %d to %d of a-z 0-9 . _ -", constants.UsernameMinLen, constants.UsernameMaxLen))
	}
	var hash string
	if takesPassword[verb] {
		pw, err := readPassword(stdin)
		if err != nil {
			return refuse(stderr, "cannot read the password from stdin")
		}
		if !password.Valid(pw) {
			return refuse(stderr, fmt.Sprintf("invalid password: %d characters to %d bytes", constants.PasswordMinChars, constants.PasswordMaxBytes))
		}
		if hash, err = h.Hash(ctx, pw); err != nil {
			return refuse(stderr, "cannot hash the password")
		}
	}

	st, err := store.Open(config.DataDir(getenv))
	if err != nil {
		return refuse(stderr, err.Error())
	}
	defer st.Close()
	now := clk.Now()
	var done string
	switch verb {
	case "create":
		_, err = st.CreateFirstUser(ctx, now, username, hash)
		done = "created"
	case "add":
		_, err = st.AddUser(ctx, now, username, hash)
		done = "added"
	case "reset-password":
		err = st.SetPassword(ctx, now, username, hash)
		done = "password reset; sessions ended"
	case "disable":
		err = st.SetDisabled(ctx, now, username, true)
		done = "disabled; sessions ended"
	case "enable":
		err = st.SetDisabled(ctx, now, username, false)
		done = "enabled"
	case "unlink":
		var n int
		n, err = st.Unlink(ctx, now, username)
		done = fmt.Sprintf("unlinked %d identities; sessions ended", n)
	}
	switch {
	case err == nil:
		fmt.Fprintln(stdout, done)
		return exitDone
	case errors.Is(err, store.ErrUsersExist):
		return refuse(stderr, "users already exist; use add")
	case errors.Is(err, store.ErrUserExists):
		return refuse(stderr, "username taken")
	case errors.Is(err, store.ErrNotFound):
		return refuse(stderr, "no such user")
	case errors.Is(err, store.ErrBusy):
		return refuse(stderr, "database busy; try again")
	default:
		return refuse(stderr, "store write failed")
	}
}

func refuse(stderr io.Writer, msg string) int {
	fmt.Fprintln(stderr, "commsgun-api admin: "+msg)
	return exitRefused
}

// readPassword strips exactly one trailing \n or \r\n; reading stops just past what Valid could accept.
func readPassword(stdin io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(stdin, int64(constants.PasswordMaxBytes+len("\r\n")+1)))
	if err != nil {
		return "", err
	}
	s := string(b)
	if t, ok := strings.CutSuffix(s, "\r\n"); ok {
		return t, nil
	}
	return strings.TrimSuffix(s, "\n"), nil
}
