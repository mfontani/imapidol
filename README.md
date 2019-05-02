# imapidol

`imapidol` is a proof-of-concept, alpha-stage but working, `IMAP` `IDLE`-based
notifier, written in Go.

## What does it do?

`imapidol` concurrently monitors one or more configured `IMAP` accounts for
changes (`MailboxUpdate`, mostly) to one given `IMAP` folder, and executes a
user-given command when such changes happen.

## What can I use it for?

You might want to be notified (i.e. via `notify-send` or `growl`) when a
given mailbox (`INBOX` or `[Gmail]/All Mail`) gets new messages, or you
might want to automatically trigger a local `IMAP` synchronisation tool
(i.e. `gmailieer` or `offlineimap`), or whatever else you think you'd
want to do when a given `IMAP` folder gets updated.

I mostly use it to be notified, so I can choose whether and when to manually
run my favourite `IMAP` synchronisation tool - at leisure.

It scratches my itch; I hope it can also scratch yours.

## How to get it

```shell
go get github.com/mfontani/imapidol
imapidol -help
```

## Caveats

I've only tested this, and care about this working on, a Linux environment.
If you care about OSX or Windows support, you're welcome to open a pull request.

The `MailboxUpdate` type of update the tool listens for is apparently triggered
both when new messages reach the watched `IMAP` folder, as well as when any
changes in message count are effected upon it (i.e. if an email gets archived
from gmail's web interface, or another IMAP program).

I've not found a way to distinguish between the two ("new email" only, vs "any
changes to this folder").

As a result, the `command` triggers both when a new message reaches the watched
folder, as well as if an already existing message gets, say, deleted and
therefore purged from the watched folder and while you might think you've got
new mail, that might not be really the case.

While the tool uses a lock file behind the scenes to ensure only one copy of it
per user is ever ran, this may not be enough for it to be properly ran via
`cron`, especially if you use `gopass` or a similar password management tool
which requires some sort of user interaction to get the accounts' passwords -
as the environment in which `cron` is ran from might not be able to show you
the required password prompt properly, and it might fail through no fault of
its own.

Or, you could use the `password_insecure` option instead, but I wouldn't
recommend doing that.

If you use a passwordless/non-interactive tool to get the password (i.e. having
a `~/.netrc` containing passwords, and a simple tool to read them), it ought to
work, although I guess that wouldn't be _that_ secure - and you might as well
just use `password_insecure`, aside from the downside of having your plain text
password in more than one place.

You can still run it in a stashed-away terminal or `tmux` session, though, so
not all hope is lost.

I don't mind having a terminal dedicated to this tool stashed away. You might.

The lock file is (by default) only triggered once the whole configuration has
been parsed and applied (including running any `password_command`). This allows
you to have an instance running with an old and working config, as well as
testing a new configuration at the same time. This doesn't help one create a
"forever loop", but you can use the `-lockearly` option, which ought to make
it possible to run something like the following, if you need to:

```bash
$ while /bin/true; imapidol -lockearly; notify-send imapidol imapidol encountered an issue; done &
```

I prefer instead something simpler, like:

```bash
$ imapidol ; notify-send imapidol stopped
```

## How to configure

You'll likely want to copy the sample INI configuration file
`config.ini.sample` into your favourite XDG directory for configuration, i.e.
`~/.config/imapidol/config.ini`.

You ought to have at least one account configured. An "account" is an INI
section (see above `[foo@bar]` in the sample config), i.e. "the stuff between
the square brackets".

Each account ought to have an `email`, a `command` (but you could provide a
global `command` in the head of the INI file, instead), and either a
`password_command` or a `password_insecure` for the account.

Don't use the `password_insecure` if you can't help it.

## Sample configuration

```ini
;folder=INBOX
folder=[Gmail]/All Mail
server=imap.example.com
; you can use the following environment variables for the command, or for the
; password_command (although the password_command isn't available in the global
; section):
; $IMAPIDOL_ACCOUNT - i.e. "foo@bar" below
; $IMAPIDOL_EMAIL - i.e. "foo@bar.example.com" below
; $IMAPIDOL_FOLDER - i.e. "Important" for the "foo@example" account below
; $IMAPIDOL_SERVER - i.e. "imap.bar.example.com" for the "foo@bar" account below
command=/usr/bin/notify-send imapidol "You've got mail on $IMAPIDOL_EMAIL!"
idle_timeout_minutes=20

[foo@bar]
email=foo@bar.example.com
server=imap.bar.example.com
password_insecure=hunter2
command=offlineimap -o -u quiet
idle_timeout_minutes=5

[foo@example]
email=foo@example.com
folder=Important
password_command=gopass email/foo@example.com
command=/usr/bin/notify-send imapidol "You've got IMPORTANT mail on $IMAPIDOL_EMAIL!"
```

## Copyright and License

`imapidol` is Copyright (c) 2019, Marco Fontani <MFONTANI@cpan.org>

It is released under the MIT license - see the `LICENSE` file in this repository/directory.
