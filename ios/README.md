# ush for iPhone and iPad

Browse and join ush sessions without an SSH app.

| Command | Does |
|:--|:--|
| `make ios-project` | generate `ios/USH.xcodeproj` with XcodeGen (`brew install xcodegen`) |
| `make ios-build` | build for the simulator (`IOS_SIM=...` to pick one) |
| `make test-ios-core` | key derivation, payload and list tests (`swift test`) |
| `make test-ios-client` | the SSH client against a local ushd with shared sessions |
| `make test-ios-ui` | the app on the simulator: tabs, ⌘ shortcuts, the Ctrl-] key |

The first build needs Xcode's Metal toolchain (SwiftTerm draws with Metal):
`xcodebuild -downloadComponent MetalToolchain`. Opening the project in Xcode
asks once to trust SwiftTerm's build plugin.

The client and UI suites start their own `ushd` on 2201/2224. If something
else already listens there, pick other ports:
`USH_DEV_PORT=2211 USH_DEV_RELAY_PORT=2234 make test-ios-client`.

To put it on a device, open `ios/USH.xcodeproj`, choose your team, and run.

`make testflight` archives and uploads to App Store Connect, where the app is
**RendezvousSH** (bundle id `net.acheris.ush`; it shows as "ush" on the home
screen). Credentials come from `~/.appstoreconnect/config.mk`, as for the other
apps.

## Pairing

On a computer with a working profile, `ush pair` (or `ush --profile work pair`).
In the app, tap **+** and scan. The code carries the seed: anyone who sees it
can join every session.

## Checked by hand on a device

- [ ] scanning `ush pair` adds the rendezvous; scanning it again replaces it
- [ ] the local-network prompt appears once, with the explanation
- [ ] on-screen keyboard: esc, ctrl+c, arrows in `less`, ^] opens the panel
- [ ] hardware keyboard: the key bar hides; ⌘1/⌘2, ⌘[ ⌘], ⌘W, ⌘O work
- [ ] a bell (`printf '\a'`) in a background tab shows 🔔 on the tab and in the tree
- [ ] switch to another app for a minute and back: tabs rejoin
- [ ] Wi-Fi off, then on: "reconnecting…", then the tab resumes
