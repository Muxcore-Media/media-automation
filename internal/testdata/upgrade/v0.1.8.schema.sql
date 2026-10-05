CREATE TABLE wanted_items (
			id             TEXT PRIMARY KEY,
			item_type      TEXT NOT NULL,
			item_id        TEXT NOT NULL,
			tmdb_id        INTEGER NOT NULL,
			title          TEXT NOT NULL,
			year           INTEGER DEFAULT 0,
			season_number  INTEGER DEFAULT 0,
			episode_number INTEGER DEFAULT 0,
			monitored      INTEGER DEFAULT 1,
			missing        INTEGER DEFAULT 1,
			quality_profile_id TEXT DEFAULT '',
			last_searched  TEXT,
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL, absolute_number INTEGER DEFAULT 0, series_type TEXT DEFAULT '', series_id TEXT DEFAULT '', clean_titles TEXT DEFAULT '[]', current_score INTEGER DEFAULT 0, file_acquired_at TEXT DEFAULT '',
			UNIQUE(item_type, item_id)
		);
CREATE TABLE download_history (
			id              TEXT PRIMARY KEY,
			wanted_item_id  TEXT NOT NULL,
			guid            TEXT NOT NULL,
			title           TEXT NOT NULL,
			indexer         TEXT DEFAULT '',
			size            INTEGER DEFAULT 0,
			score           INTEGER DEFAULT 0,
			download_url    TEXT DEFAULT '',
			download_protocol TEXT DEFAULT '',
			status          TEXT DEFAULT 'pending',
			sent_at         TEXT,
			completed_at    TEXT,
			created_at      TEXT NOT NULL,
			download_id     TEXT DEFAULT ''
		);
CREATE TABLE delay_profiles (
			protocol     TEXT PRIMARY KEY,
			wait_minutes INTEGER NOT NULL DEFAULT 0
		);
CREATE TABLE release_seen (
			guid          TEXT PRIMARY KEY,
			first_seen_at TEXT NOT NULL
		);
CREATE TABLE series_overrides (
			series_id         TEXT PRIMARY KEY,
			delay_minutes     INTEGER,
			preferred_groups  TEXT NOT NULL DEFAULT '',
			ignored_groups    TEXT NOT NULL DEFAULT '',
			updated_at        TEXT NOT NULL
		);
CREATE INDEX idx_download_history_download_id ON download_history(download_id)
	;
CREATE INDEX idx_wanted_type ON wanted_items(item_type, missing)
	;
