# Terminal demo for the README. Record with:
#   docs/demo/env.sh up && docs/demo/env.sh tape
# VHS runs inside the demo "laptop" container, so nothing touches your machine.
# Top pane holds the tunnel, bottom pane is where you work.

Output media/terminal.gif
Output media/terminal.mp4

Set Shell "bash"
Set FontSize 17
Set FontFamily "JetBrains Mono"
Set Width 1280
Set Height 860
Set Padding 18
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms
Set WindowBar Colorful

Hide
Type "sudo -iu dev"
Enter
Type "set -a; . /demo/.env; PGCONNECT_TIMEOUT=3; set +a"
Enter
Type `tmux new-session \; split-window -v -l 50% \; select-pane -t 0`
Enter
Sleep 2s
Show

# bottom pane: the database is private
Ctrl+B
Down
Type "psql -h 10.20.0.5 -U app orders -c 'select * from orders'"
Sleep 400ms
Enter
Sleep 4s

# top pane: first connect, saved as a profile
Ctrl+B
Up
Type "sudo auth-vpn connect 203.0.113.10:7777 --token $STAGE_TOKEN"
Sleep 400ms
Enter
Sleep 1.2s
Type "stage"
Sleep 600ms
Enter
Sleep 2s
Type "y"
Sleep 400ms
Enter
Sleep 3s

# bottom pane: now it answers
Ctrl+B
Down
Type "clear; psql -h 10.20.0.5 -U app orders -c 'select * from orders'"
Enter
Sleep 2.5s

# add prod, then pick it from the menu: the stage tunnel stops by itself
Type "clear; auth-vpn profile add prod 198.51.100.7 --token $PROD_TOKEN"
Enter
Sleep 2s
Type "clear; sudo auth-vpn connect"
Enter
Sleep 2.5s
Type "2"
Sleep 300ms
Enter
Sleep 3s
Type "y"
Enter
Sleep 3s

# top pane is free again: prod data, and status
Ctrl+B
Up
Type "clear; psql -h 10.30.0.5 -U app orders -c 'select * from orders'"
Enter
Sleep 2.5s
Type "auth-vpn status"
Enter
Sleep 4s
