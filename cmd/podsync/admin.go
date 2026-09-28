package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/mxpv/podsync/services/admin"
)

// adminRuntime exposes the running configuration and schedule to the admin interface.
type adminRuntime struct {
	reloader *configReloader
	schedule *feedSchedule
}

func (r adminRuntime) Feeds() []admin.FeedRuntime {
	cfg := r.reloader.Current()
	feeds := make([]admin.FeedRuntime, 0, len(cfg.Feeds))
	for id, feedConfig := range cfg.Feeds {
		runtime := admin.FeedRuntime{Config: feedConfig, Schedule: feedCronSchedule(feedConfig)}
		if scheduled, ok := r.schedule.Lookup(id); ok {
			runtime.Schedule = scheduled.Spec
			runtime.NextRun = scheduled.NextRun
		}
		feeds = append(feeds, runtime)
	}
	return feeds
}

// runHashPassword prints a bcrypt hash for admin.password_hash. On a terminal it prompts twice
// without echo; otherwise it reads one line from stdin, for scripting.
func runHashPassword(stdin *os.File, stdout, stderr io.Writer) error {
	var password string
	if term.IsTerminal(int(stdin.Fd())) {
		fmt.Fprint(stderr, "Admin password: ")
		first, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return errors.Wrap(err, "failed to read password")
		}
		fmt.Fprint(stderr, "Repeat password: ")
		second, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return errors.Wrap(err, "failed to read password")
		}
		if string(first) != string(second) {
			return errors.New("passwords do not match")
		}
		password = string(first)
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return errors.Wrap(err, "failed to read password")
		}
		password = strings.TrimRight(line, "\r\n")
	}

	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, hash)
	fmt.Fprintln(stderr, "Add it to your configuration:\n\n[admin]\nauth = \"password\"\npassword_hash = \""+hash+"\"")
	return nil
}

// minAdminPasswordLength guards against trivially guessable admin passwords.
const minAdminPasswordLength = 12

func hashAdminPassword(password string) (string, error) {
	if len(password) < minAdminPasswordLength {
		return "", errors.Errorf("the admin password must be at least %d characters", minAdminPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", errors.Wrap(err, "failed to hash password")
	}
	return string(hash), nil
}
