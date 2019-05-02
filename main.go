package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"

	"gopkg.in/ini.v1"

	"github.com/adrg/xdg"

	"github.com/fredli74/lockfile"
)

// (almost) any section can contain these keys
var validINIKeys = []string{
	"command",
	"email",
	"folder",
	"password_command",
	"password_insecure",
	"server",
	"idle_timeout_minutes",
}

// These keys are REQUIRED in all sections BUT DEFAULT
var requiredINIKeys = []string{
	// "command" isn't required in all sections, as one might want to add it
	// to the DEFAULT section and just have one command for all of them.
	// "command",
	"email",
}

// These keys are REQUIRED in all sections BUT DEFAULT, either/or
var requiredINIKeysEitherOr = []string{
	"password_command",
	"password_insecure",
}

// These keys are invalid in the DEFAULT section
var invalidINIKeysDefault = []string{
	"email",
	"password_insecure",
	// TODO this could be specified once in the DEFAULT section, and if one's
	// gopass setup can make use of the $IMAPIDOL_EMAIL or w/e.. it should reduce
	// the amount of configuratio lines required.
	"password_command",
}

func printUsage() {
	_, err := fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS]\n", os.Args[0])
	if err != nil {
		panic(err)
	}
	flag.PrintDefaults()
}

// The INI file name
var iniFileName = "imapidol/config.ini"

// The lock file name/location
var lockFileName = "imapidol/imapidol.pid"

// The INI file to parse configuration from
var iniFile string

// Whether to dump the configuration and exit
var wantDumpConfig bool

// Whether to dump the passwords as plain text; only valid with wantDumpConfig
var wantDumpPassword bool

// Whether to dump debugging statements
var wantDebug bool

// Whether to show verbose output (on by default for wantDebug)
var wantVerbose bool

// Whether to "lock early" (i.e for scheduling a "forever loop")
var wantLockEarly bool

// Which IMAP folder to watch by default
var defaultFolder = "INBOX"

// ... if no flag from the command line has been given:
var flagFolder string

// Which IMAP server to connect to by default
var defaultServer = "imap.gmail.com:993"

// Minutes to max idle for (as a string, to simplify things)
var defaultIdleTimeoutMinutes = "15"

// ... if no flag from the command line has been given:
var flagServer string

// Which command to execute by default
var defaultCommand = ""

// ... if no flag from the command line has been given:
var flagCommand string

// IMAPIDOLAccount contains an IMAPIDOL account's whole configuration
type IMAPIDOLAccount struct {
	Account            string
	Command            string
	Email              string
	Folder             string
	PasswordCommand    string
	PasswordInsecure   string
	Server             string
	IdleTimeoutMinutes int
}

// IMAPIDOL is the list of accounts to keep track of IDLE connections for
var IMAPIDOL []IMAPIDOLAccount

// Dump the INI configuration from the given INI file
func dumpINIConfig(cfg *ini.File) {
	for i, s := range cfg.Sections() {
		fmt.Printf("%d) Section %s\n", i, s.Name())
		for j, k := range s.Keys() {
			val := k.Value()
			if k.Name() == "password_insecure" && !wantDumpPassword {
				val = "XXX (use -dumppasswordsasplaintext to show!)"
			}
			fmt.Printf("  %d) %s = %s\n", j, k.Name(), val)
		}
	}
}

// Dump the IMAPIDOL object
func dumpIMAPIDOL(idol []IMAPIDOLAccount) {
	for i, a := range idol {
		fmt.Printf("%d) Account %s\n", i, a.Account)
		fmt.Printf("  IdleTimeoutMinutes %d\n", a.IdleTimeoutMinutes)
		fmt.Printf("  Server %s\n", a.Server)
		fmt.Printf("  Email %s\n", a.Email)
		val := "XXX (use -dumppasswordsasplaintext to show!)"
		if wantDumpPassword {
			val = a.PasswordInsecure
		}
		fmt.Printf("  PasswordInsecure %s\n", val)
		fmt.Printf("  Folder %s\n", a.Folder)
		fmt.Printf("  Command %s\n", a.Command)
	}
}

// Validate that the INI file has a good configuration
func validateConfig(cfg *ini.File) {
	err := validateConfigWithErr(cfg)
	if err != nil {
		fmt.Printf("Failed to validate file %v: %v\n", iniFile, err)
		os.Exit(1)
	}
}
func validateConfigWithErr(cfg *ini.File) error {
	for _, s := range cfg.Sections() {
		// Check that all keys are valid
		for _, k := range s.Keys() {
			found := false
			for _, kk := range validINIKeys {
				if kk == k.Name() {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("section %s has invalid key %s (=%s). Valid INI section keys: %v", s.Name(), k.Name(), k.Value(), validINIKeys)
			}
		}
		// Check that required keys are set
		for _, rk := range requiredINIKeys {
			found := false
			for _, k := range s.Keys() {
				if rk == k.Name() {
					found = true
					break
				}
			}
			// DEFAULT section doesn't REQUIRE those values set.
			if !found && s.Name() != "DEFAULT" {
				return fmt.Errorf("section %s does not contain required key %s", s.Name(), rk)
			}
		}
		// Check that required either/or keys are set
		foundEitherOr := 0
		for _, k := range s.Keys() {
			for _, rk := range requiredINIKeysEitherOr {
				if rk == k.Name() {
					foundEitherOr++
				}
			}
		}
		// DEFAULT section doesn't REQUIRE those values set.
		if foundEitherOr > 1 && s.Name() != "DEFAULT" {
			return fmt.Errorf("section %s contains more than one key in %v. Only one is supported (either/or)", s.Name(), requiredINIKeysEitherOr)
		}
		if foundEitherOr == 0 && s.Name() != "DEFAULT" {
			return fmt.Errorf("section %s requires a setting for %v. None are currently set", s.Name(), requiredINIKeysEitherOr)
		}
		// The DEFAULT section doesn't NEED anything, but doesn't support some keys:
		if s.Name() == "DEFAULT" {
			for _, ik := range invalidINIKeysDefault {
				found := false
				for _, k := range s.Keys() {
					if ik == k.Name() {
						found = true
						break
					}
				}
				if found {
					return fmt.Errorf("section %s does not support key %s", s.Name(), ik)
				}
			}
		}
	}
	return nil
}

func getOverallDefault(cfg *ini.File, ourDefault string, flagDefault string, iniKey string) string {
	// command-line given flag always trumps
	if len(flagDefault) != 0 {
		return flagDefault
	}
	// INI key flag always trumps
	var ourINIValue = cfg.Section("").Key(iniKey).String()
	if len(ourINIValue) != 0 {
		return ourINIValue
	}
	// Our default is otherwise used
	return ourDefault
}

func getSectionValue(section *ini.Section, key string, defaultValue string) string {
	sectionValue := section.Key(key).String()
	if len(sectionValue) != 0 {
		return sectionValue
	}
	return defaultValue
}

// Apply the INI config to the given IMAPIDOL object
func applyConfig(cfg *ini.File, ii *[]IMAPIDOLAccount) {
	err := applyConfigWithErr(cfg, ii)
	if err != nil {
		fmt.Printf("Failed to apply config from file %v: %v\n", iniFile, err)
		os.Exit(1)
	}
}

func applyConfigWithErr(cfg *ini.File, ii *[]IMAPIDOLAccount) error {
	var ourDefaultServer = getOverallDefault(cfg, defaultServer, flagServer, "server")
	if wantDebug {
		fmt.Printf("Default IMAP server: %v\n", ourDefaultServer)
	}
	var ourDefaultFolder = getOverallDefault(cfg, defaultFolder, flagFolder, "folder")
	if wantDebug {
		fmt.Printf("Default IMAP folder: %v\n", ourDefaultFolder)
	}
	var ourDefaultCommand = getOverallDefault(cfg, defaultCommand, flagCommand, "command")
	if wantDebug {
		fmt.Printf("Default command: %v\n", ourDefaultCommand)
	}
	var ourDefaultIdleTimeoutMinutes = getOverallDefault(cfg, defaultIdleTimeoutMinutes, "", "idle_timeout_minutes")
	if wantDebug {
		fmt.Printf("Default minutes IDLE timeout: %v\n", ourDefaultIdleTimeoutMinutes)
	}
	// For each non-DEFAULT section found, create an IMAPIDOLAccount instance
	// containing the "right" configuration for that account:
	// - default to the above default, or
	// - override with instance-specific values
	var foundSections = 0
	for _, s := range cfg.Sections() {
		if s.Name() != "DEFAULT" {
			var acct = IMAPIDOLAccount{}
			acct.Account = s.Name()
			// There is no default email, password_insecure or password_command
			acct.Email = getSectionValue(s, "email", "")
			acct.PasswordInsecure = getSectionValue(s, "password_insecure", "")
			acct.PasswordCommand = getSectionValue(s, "password_command", "")
			// These instead can have values trickle down
			acct.Command = getSectionValue(s, "command", ourDefaultCommand)
			if len(acct.Command) == 0 {
				return fmt.Errorf("account %s (%s) does not have a command set", acct.Account, acct.Email)
			}
			acct.Folder = getSectionValue(s, "folder", ourDefaultFolder)
			acct.Server = getSectionValue(s, "server", ourDefaultServer)
			// The timeout's a string, so far, which needs to be converted to an int for usage.
			idleTimeoutMinutesVal := getSectionValue(s, "idle_timeout_minutes", ourDefaultIdleTimeoutMinutes)
			idleTimeoutMinutes, err := strconv.Atoi(idleTimeoutMinutesVal)
			if err != nil {
				return fmt.Errorf("account %s (%s) has a bad number for idle_timeout_minutes %s: %v", acct.Account, acct.Email, idleTimeoutMinutesVal, err)
			}
			if idleTimeoutMinutes <= 0 {
				return fmt.Errorf("account %s (%s) has a bad <= 0 number for idle_timeout_minutes %s", acct.Account, acct.Email, idleTimeoutMinutesVal)
			}
			// https://tools.ietf.org/html/rfc2177
			// [...] clients using IDLE are advised to terminate the IDLE and
			// re-issue it at least every 29 minutes to avoid being logged off.
			if idleTimeoutMinutes > 29 {
				return fmt.Errorf("account %s (%s) has a bad > 29 number for idle_timeout_minutes %s", acct.Account, acct.Email, idleTimeoutMinutesVal)
			}
			acct.IdleTimeoutMinutes = idleTimeoutMinutes
			// Ensure we always have a acct.PasswordInsecure to log in with,
			// AHEAD of creating goroutines for handling accounts.
			if len(acct.PasswordInsecure) == 0 {
				if wantVerbose {
					fmt.Printf("Executing password command for %s\n  /bin/sh -c '%s'\n", acct.Account, acct.PasswordCommand)
				}
				cmd := exec.Command("/bin/sh", "-c", acct.PasswordCommand)
				appendIMAPIDOLAccountEnvironment(acct, cmd)
				var out bytes.Buffer
				var stderr bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &stderr
				err := cmd.Run()
				if err != nil {
					return fmt.Errorf("Error executing password command for %s (%s): %v %s", acct.Account, acct.PasswordCommand, err, stderr.String())
				}
				if wantDebug {
					log.Printf("Executed password command OK for %s", acct.Account)
				}
				acct.PasswordInsecure = out.String()
				acct.PasswordCommand = ""
			}
			*ii = append(*ii, acct)
			foundSections++
		}
	}
	if foundSections == 0 {
		return fmt.Errorf("There are no sections to work on")
	}
	return nil
}

func appendIMAPIDOLAccountEnvironment(account IMAPIDOLAccount, cmd *exec.Cmd) {
	cmd.Env = append(os.Environ(),
		"IMAPIDOL_ACCOUNT="+account.Account,
		"IMAPIDOL_EMAIL="+account.Email,
		"IMAPIDOL_FOLDER="+account.Folder,
		"IMAPIDOL_SERVER="+account.Server,
	)
}

type writeLogger struct {
	prefix string
	w      io.Writer
}

func (l *writeLogger) Write(p []byte) (n int, err error) {
	n, err = l.w.Write(p)
	if err != nil {
		log.Printf("%s %x: %v", l.prefix, p[0:n], err)
	} else {
		log.Printf("%s %x", l.prefix, p[0:n])
	}
	return
}

func newWriteLogger(prefix string) io.Writer {
	return &writeLogger{prefix, os.Stderr}
}

func handleAccount(account IMAPIDOLAccount, wg *sync.WaitGroup, shutdownCh chan struct{}) {
	defer wg.Done()

	// for logging what's what.
	what := account.Account + " / " + account.Server + " / " + account.Email

	// Connect
	if wantDebug {
		log.Printf("Connecting to %s\n", account.Server)
	}
	// should prevent a sorta kinda memory leak
	tlsConfig := &tls.Config{SessionTicketsDisabled: true}
	c, err := client.DialTLS(account.Server, tlsConfig)
	if err != nil {
		log.Fatalf("Error connecting to %s: %v", what, err)
	}
	if wantDebug {
		log.Printf("Connected to %s\n", what)
	}
	if wantDebug {
		l := newWriteLogger(what + ":")
		c.SetDebug(l)
	}

	// Log in using the insecure password, which has already been populated.
	err = c.Login(account.Email, account.PasswordInsecure)
	if err != nil {
		log.Fatalf("Error logging in to %s: %v", what, err)
	}
	if wantDebug {
		log.Printf("Successfully logged in to %s\n", what)
	}

	// Select the folder to check IDLE for
	if _, err := c.Select(account.Folder, false); err != nil {
		log.Fatalf("Error selecting folder %s for %s: %v", account.Folder, what, err)
	}
	idleClient := idle.NewClient(c)

	// Lower logout timeout to configured per-account value
	idleClient.LogoutTimeout = time.Duration(account.IdleTimeoutMinutes) * time.Minute

	// channel to receive updates on
	updates := make(chan client.Update)
	c.Updates = updates

	// Start idling
	done := make(chan error, 1)
	go func() {
		done <- idleClient.IdleWithFallback(nil, idleClient.LogoutTimeout)
	}()

	// Listen for updates
	if wantVerbose {
		fmt.Printf("Listening for updates to %s for %s\n", account.Folder, what)
	}
	for {
		select {
		case <-shutdownCh:
			if wantVerbose {
				fmt.Printf("Got shutdown for %s...\n", what)
			}
			return
		case update := <-updates:
			// Only look for "client.MailboxUpdate" (new mail)!
			updateType := reflect.TypeOf(update).String()
			if wantDebug {
				s, _ := json.MarshalIndent(update, "", "  ")
				log.Printf("New update for %s type %s: %s", what, updateType, string(s))
			}
			switch update.(type) {
			case *client.MailboxUpdate:
				if wantDebug {
					mbx := update.(*client.MailboxUpdate)
					s, _ := json.MarshalIndent(mbx, "", "  ")
					log.Printf("MailboxUpdate for %s type %s: %s", what, updateType, string(s))
				}
				if wantVerbose {
					fmt.Printf("Notifying for %s on %s via /bin/sh -c '%s'\n", account.Folder, what, account.Command)
				}
				// fork/exec /usr/bin/notify-send test: no such file or directory
				// but a simple /bin/sh -c '...' works.
				cmd := exec.Command("/bin/sh", "-c", account.Command)
				appendIMAPIDOLAccountEnvironment(account, cmd)
				var out bytes.Buffer
				var stderr bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &stderr
				err := cmd.Run()
				if err != nil {
					log.Fatalf("Error executing notification command for %s (%s): %v %s", what, account.Command, err, stderr.String())
				}
				if wantDebug {
					log.Printf("Notification command executed for %s (%s): %s", what, account.Command, out.String())
				}
			default:
				if wantDebug {
					log.Printf("Not notifying for update type %s for %s", updateType, what)
				}
			}
		case err := <-done:
			if err != nil {
				log.Fatalf("Error retrieving updates for %s (%s): %v", what, account.Folder, err)
			}
			log.Printf("Account %s not idling anymore", what)
			return
		}
	}
}

func runIMAPIDOL(ii []IMAPIDOLAccount) {
	shutdownCh := make(chan struct{})
	var wg = &sync.WaitGroup{}

	for _, s := range ii {
		if wantDebug {
			log.Printf("Creating connection for %s...", s.Account)
		}
		wg.Add(1)
		go func(_s IMAPIDOLAccount) {
			handleAccount(_s, wg, shutdownCh)
			if wantDebug {
				log.Printf("Done handleAccount() for %s", _s.Account)
			}
		}(s)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	if wantVerbose {
		fmt.Printf("Running.\n")
	}
	<-c
	if wantVerbose {
		fmt.Printf("Got the shutdown signal...\n")
	}
	close(shutdownCh)
	wg.Wait()
}

func setupFlags() {
	flag.Usage = printUsage
	flag.StringVar(&iniFile, "ini", "", "the INI configuration file to use, default is "+iniFileName+" from XDG dirs")
	flag.StringVar(&flagFolder, "folder", "", "the default IMAP folder to watch (overrides INI's DEFAULT), defaults to "+defaultFolder)
	flag.StringVar(&flagServer, "server", "", "the default IMAP server:port to connect to (overrides INI's DEFAULT), defaults to "+defaultServer)
	flag.StringVar(&flagCommand, "command", "", "the default command to execute (overrides INI's DEFAULT if given), defaults to "+defaultCommand)
	flag.BoolVar(&wantDumpConfig, "dumpconfig", false, "whether to dump the INI + applied configuration and exit")
	flag.BoolVar(&wantDumpPassword, "dumppasswordsasplaintext", false, "also dump the actual passwords; implies -dumpconfig")
	flag.BoolVar(&wantDebug, "debug", false, "whether to print debugging statements")
	flag.BoolVar(&wantVerbose, "verbose", false, "whether to print verbose statements (default on for debug)")
	flag.BoolVar(&wantLockEarly, "lockearly", false, "whether to check for lock early, i.e. before applying config")
	flag.Parse()
	if wantDebug {
		wantVerbose = true
	}
	if wantDumpPassword {
		wantDumpConfig = true
	}
}

func getConfig() *ini.File {
	if len(iniFile) == 0 {
		// Try to find the iniFileName in the XDG directories.
		xdgINIFile, err := xdg.SearchConfigFile(iniFileName)
		if err != nil {
			fmt.Printf("Failed to find %s in XDG dirs. Use -ini FILE?", iniFileName)
			os.Exit(1)
		}
		iniFile = xdgINIFile
	}
	cfg, err := ini.Load(iniFile)
	if err != nil {
		fmt.Printf("Failed to read file %v: %v\n", iniFile, err)
		os.Exit(1)
	}
	return cfg
}

func grabLockOrExit() *lockfile.LockFile {
	// Try and find the lock file...
	lockFilePath, err := xdg.SearchRuntimeFile(lockFileName)
	if err != nil {
		if wantDebug {
			log.Printf("Lock file %s not found. Creating anew.", lockFileName)
		}
		// Create it anew
		newLockFilePath, err2 := xdg.RuntimeFile(lockFileName)
		if err2 != nil {
			fmt.Printf("Failed to create lock file %s: %v", lockFileName, err2)
			os.Exit(1)
		}
		lockFilePath = newLockFilePath
		if wantDebug {
			log.Printf("Created lock file %s", lockFilePath)
		}
	}
	if wantDebug {
		log.Printf("Using lock file %s", lockFilePath)
	}
	lock, err := lockfile.Lock(lockFilePath)
	if err != nil {
		// Another process is running.
		if wantVerbose {
			log.Printf("Exiting, as another process holds the lock on %s: %v", lockFilePath, err)
		}
		os.Exit(0)
	}
	if wantDebug {
		log.Printf("Locked lock file %s", lockFilePath)
	}
	return lock
}

func main() {
	setupFlags()
	cfg := getConfig()
	validateConfig(cfg)
	if wantDumpConfig {
		fmt.Printf("Configuration from INI file %s:\n", iniFile)
		dumpINIConfig(cfg)
	}
	if wantLockEarly {
		lock := grabLockOrExit()
		defer lock.Unlock()
	}
	applyConfig(cfg, &IMAPIDOL)
	if wantDumpConfig {
		fmt.Printf("Applied accounts' configuration:\n")
		dumpIMAPIDOL(IMAPIDOL)
		os.Exit(0)
	}
	if !wantLockEarly {
		lock := grabLockOrExit()
		defer lock.Unlock()
	}
	runIMAPIDOL(IMAPIDOL)
	if wantVerbose {
		fmt.Printf("Bye!\n")
	}
}
