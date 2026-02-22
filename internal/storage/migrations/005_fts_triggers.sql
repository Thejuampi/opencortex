PRAGMA foreign_keys = ON;

CREATE TRIGGER IF NOT EXISTS knowledge_ai AFTER INSERT ON knowledge_entries BEGIN
  INSERT INTO knowledge_fts(rowid, title, content, summary, tags)
  VALUES (new.rowid, new.title, new.content, new.summary, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS knowledge_ad AFTER DELETE ON knowledge_entries BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, content, summary, tags)
  VALUES ('delete', old.rowid, old.title, old.content, old.summary, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS knowledge_au AFTER UPDATE ON knowledge_entries BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, content, summary, tags)
  VALUES ('delete', old.rowid, old.title, old.content, old.summary, old.tags);
  INSERT INTO knowledge_fts(rowid, title, content, summary, tags)
  VALUES (new.rowid, new.title, new.content, new.summary, new.tags);
END;

