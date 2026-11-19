# Changelog

## v1.1.0 (2026-11-19)

#### Features

- imapsync: fetch conversation bodies together and prefetch new INBOX mail (e9785f8)

#### Bug Fixes

- imapsync: don't let shutdown wait on a dead connection (0907bde)
- propagate base64 write errors in compose; let the GUI open ftp links (268eb6f)
- foonmaild: reject malformed flags over D-Bus (0a89e90)
- foonmaild: don't block on the hub's channels after run returns (8c48ace)
- chmod the cache and outbox database files to 0600 when opening them (3e1a851)

#### Documentation

- fix stale comments about the tray and the undo window (cf21204)

#### Tests

- wait for the async daemon flag calls in TestToggleFlagged (f083dd0)

#### Continuous Integration

- install libgles-dev and libegl-dev instead of the Mesa transitional packages (722bcf0)
- update checkout and setup-go to v7, which run on Node.js 24 (ae4639a)

## v1.0.0 (2026-11-11)

#### Features

- version the programs and release from v* tags with foonver (9571dd1)
- gui: log where the event loop is stuck when it stalls (0d5f746)
- quotes: read forwards, signatures below quotes and US Gmail attributions (6baa98c)
- fold long signatures to two lines, shown in full with h (6d12b11)
- rebuild missing earlier messages of a conversation from the quote (bea9d7f)
- r replies to all, Shift+R to the sender only (a304a01)
- open a notified message as the current one; show new messages as new (28953ed)
- add a help view with every key: ? (F1 while writing) (c07f8f9)
- mail filters: rules kept on the server as Sieve, over ManageSieve (f55b1f4)
- server search: show the folder being searched, skip one that stalls (c2274d8)
- server search: Ctrl+Enter in the filter searches every folder (18d0dd5)
- imap-probe: -search times a header and a full-text search per folder (98cdc33)
- sync at once when the network comes up, and on request (c187b0e)
- add ! to flag and unflag conversations (93e9627)
- match spam look-alikes by subject alone too (c952c2b)
- create and delete folders from the folder pane (7acbdf1)
- treat duplicate deliveries of a message as one, with every copy (047943c)
- add u to show only unread conversations (8255694)
- after junking, offer the rest of the spam run (f087201)
- mark several conversations, move to any folder, and keep the list steady (069cc53)
- show senders' whole names in the list and conversation header (aa696f1)
- hide the selected conversation while a filter is typed (ac04997)
- make the undo delay 5 seconds and configurable (a4150af)
- allow an alias for a whole domain (926b83e)
- show emoji in color (0c08293)
- add foonmailctl passwords, to check and store passwords in the keyring (1fed8f7)
- support Gmail with an app password (adf8dba)
- fold other accounts in the folder pane, and switch accounts with Tab (a311a88)
- show every account in the window (5842f94)
- suggest addresses, add signatures, and autosave what is being written (437394b)
- color only the number in link and attachment lists (8337276)
- open and save attachments, and show a mail's HTML in the browser (b905583)
- format the open conversation's fields (5b0b7af)
- keep a link's number across a conversation's messages (49f41b7)
- move between messages with ↑ and ↓, scrolling long ones first (dc6e3dc)
- color links, quotes and signatures; mark the current message (87e8707)
- number and shorten web addresses in plain text, and mark link spans (08ab368)
- send text as format=flowed (c4154d5)
- archive, junk and trash with a, s and d (or Delete) (224b984)
- open messages from notifications; install with a desktop file and systemd (e3295ff)
- order special folders after INBOX in the folder pane (3284ceb)
- make the folder pane respond to the mouse (b3f8dfb)
- open folders in a sliding pane beside the list (6f42f98)
- continue drafts, and save them with Ctrl+S (82d3985)
- compose, reply and forward in the GUI (5111ce5)
- add internal/compose: replies, forwards and message building (fb56bd6)
- send mail from foonmaild, with undo from the notification (e1957fa)
- add the outbox: SMTP submission with undo and crash safety (9fc592f)
- make ←/→ move between conversations, like k/j (941c254)
- move between messages with ←/→ in the conversation view (468ae85)
- show conversations and add a folder picker to the GUI (addc081)
- add the foonmail GUI: INBOX list and message view (ba92516)
- add the daemon's D-Bus interface for the GUI (8c51569)
- show where redirect links claim to go (f79aa3c)
- add render.ShortURL for the link list (b9755b5)
- add message renderer and render-sample tool (5306676)
- add tray icon and desktop notifications to foonmaild (8011145)
- add foonmaild daemon (6b45ead)
- add imapsync: keep the cache in step with the server (edd80ac)
- add SQLite mail cache (6bbf4d3)
- add TOML config loading and validation (89b00e1)
- add imap-probe: capabilities, folders, IDLE and resync (ea0b929)
- add Gio compose and message list prototypes (8876044)

#### Bug Fixes

- decode encoded words that senders put inside quoted names (9448dae)
- rebuilt messages: don't let the reply itself stop the chain (fc6cb2f)
- don't hide text in font-size:0 layout wrappers (bdd83b8)
- put the / filter back after the look-alikes (b1415c3)
- return to the same place in the list after the look-alikes (4eab8bb)
- decode headers in every charset, and keep the emoji font in list rows (2679ee3)
- use the HTML part when the text part is broken (1c6f86e)
- expand tabs in message text (371f777)
- leave links only in collapsed quotes out of the link list (cc8e372)
- stop the GUI and daemon from raising each other forever (4ec7e88)
- show the new-mail tray icon instead of a placeholder (5f3803a)
- show mail to yourself once in a conversation (da7b265)

#### Documentation

- add a README (9fa8a9d)

#### Tests

- add benchmark for storing a sync chunk in the cache (3ed1854)

#### Build System

- add a Makefile (e3589f2)

#### Maintenance

- license under the GPL, version 3 or later (21fc05f)
- remove personal data from the repository (3fd72cf)
- make IDLE debug logs readable (1718028)
