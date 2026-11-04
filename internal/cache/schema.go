package cache

// migrations[i] moves the schema from version i to i+1. The current
// version is kept in PRAGMA user_version. Never edit a migration that has
// shipped; add a new one.
var migrations = []string{
	// 1: initial schema.
	`
CREATE TABLE accounts (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE folders (
	id             INTEGER PRIMARY KEY,
	account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name           TEXT NOT NULL,
	delimiter      TEXT NOT NULL DEFAULT '',
	special_use    TEXT NOT NULL DEFAULT '',  -- e.g. \Sent; '' when none
	uid_validity   INTEGER NOT NULL DEFAULT 0, -- 0: never synced
	highest_modseq INTEGER NOT NULL DEFAULT 0,
	uid_next       INTEGER NOT NULL DEFAULT 0,
	synced_at      INTEGER NOT NULL DEFAULT 0, -- unix seconds
	UNIQUE (account_id, name)
);

-- A message, stored once per account even when it is in several folders
-- (or carries several Gmail labels). Where it is lives in locations.
CREATE TABLE messages (
	id          INTEGER PRIMARY KEY,
	account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	message_id  TEXT NOT NULL DEFAULT '', -- Message-ID header, without <>
	subject     TEXT NOT NULL DEFAULT '',
	from_name   TEXT NOT NULL DEFAULT '',
	from_addr   TEXT NOT NULL DEFAULT '',
	to_list     TEXT NOT NULL DEFAULT '', -- "Name <addr>, ..." for display and search
	cc_list     TEXT NOT NULL DEFAULT '',
	date        INTEGER NOT NULL DEFAULT 0, -- unix seconds
	in_reply_to TEXT NOT NULL DEFAULT '',
	refs        TEXT NOT NULL DEFAULT '', -- References, space-separated, without <>
	size        INTEGER NOT NULL DEFAULT 0, -- RFC822.SIZE
	raw         BLOB,                       -- NULL until fetched
	fetched_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_by_message_id ON messages (account_id, message_id) WHERE message_id != '';

-- Where a message is: one row per folder and UID. Flags are per folder in IMAP.
CREATE TABLE locations (
	folder_id  INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
	uid        INTEGER NOT NULL,
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	flags      TEXT NOT NULL DEFAULT '', -- sorted, space-separated
	modseq     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (folder_id, uid)
) WITHOUT ROWID;
CREATE INDEX locations_by_message ON locations (message_id);

-- Full-text search. rowid is messages.id. Diacritics are kept, so that
-- "käsi" and "kasi" stay different words; prefix indexes serve search as
-- you type.
CREATE VIRTUAL TABLE message_text USING fts5 (
	subject, people, body,
	tokenize = 'unicode61 remove_diacritics 0',
	prefix = '2 3'
);

CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
	INSERT INTO message_text (rowid, subject, people, body)
	VALUES (new.id, new.subject,
	        new.from_name || ' ' || new.from_addr || ' ' || new.to_list || ' ' || new.cc_list, '');
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
	DELETE FROM message_text WHERE rowid = old.id;
END;
`,
}
