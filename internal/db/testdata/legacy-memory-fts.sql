-- Historical runtime FTS derived structure shared by both legacy schema commits.
-- Source: internal/db/memory_fts.go at 49d5f878ebfe0bc9b1a7313cbf19b9623697d73a, 7db2f863a5953a610c27cffab7a40140462ee151
-- Source SHA-256: 23156cbf60698b82690d1062914643324119fdf5834b936b345ce6a7a2f488a2
CREATE TABLE kaguya_memory_index_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE VIRTUAL TABLE kaguya_memory_fts USING fts5(
    title_terms, alias_terms, summary_terms, body_terms,
    content='kaguya_memory_search_doc',
    content_rowid='id',
    tokenize='unicode61',
    detail=full
);

CREATE TRIGGER kaguya_memory_search_ai
AFTER INSERT ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        new.id, new.title_terms, new.alias_terms, new.summary_terms, new.body_terms
    );
END;

CREATE TRIGGER kaguya_memory_search_ad
AFTER DELETE ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        kaguya_memory_fts, rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        'delete', old.id, old.title_terms, old.alias_terms, old.summary_terms, old.body_terms
    );
END;

CREATE TRIGGER kaguya_memory_search_au
AFTER UPDATE ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        kaguya_memory_fts, rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        'delete', old.id, old.title_terms, old.alias_terms, old.summary_terms, old.body_terms
    );
    INSERT INTO kaguya_memory_fts(
        rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        new.id, new.title_terms, new.alias_terms, new.summary_terms, new.body_terms
    );
END;
INSERT INTO kaguya_memory_index_meta (key,value) VALUES ('fts_schema_version','1');
