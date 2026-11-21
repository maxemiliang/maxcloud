-- claude-history's schema. The image's entrypoint runs this on every start
-- (CLICKHOUSE_ALWAYS_RUN_INITDB_SCRIPTS), as the admin, before the server
-- listens on anything but localhost. So every statement must be safe to run
-- again: CREATE ... IF NOT EXISTS for new objects, and for a change to an
-- existing table, an ALTER TABLE ... IF NOT EXISTS (or IF EXISTS) appended
-- here, never an edit of the CREATE TABLE.

CREATE DATABASE IF NOT EXISTS claude;

-- One row per line of a Claude Code transcript (~/.claude/projects/<project>/
-- <session>.jsonl, and subagents' transcripts below it). Vector sends three
-- fields: host, file and raw, the line as Claude Code wrote it. Everything
-- else is read from raw on insert. The transcript format isn't documented
-- and changes between Claude Code versions: to read a field differently,
-- change its expression with ALTER TABLE ... MODIFY COLUMN, then ALTER TABLE
-- ... MATERIALIZE COLUMN to rewrite the rows already stored.
CREATE TABLE IF NOT EXISTS claude.events
(
    host LowCardinality(String),
    file String,
    raw String CODEC(ZSTD(3)),

    type LowCardinality(String) MATERIALIZED JSONExtractString(raw, 'type'),
    -- Some records (file-history-snapshot) have no sessionId; the file is
    -- named after the session (subagents: <session>/subagents/agent-*.jsonl).
    session_id String MATERIALIZED if(JSONExtractString(raw, 'sessionId') != '', JSONExtractString(raw, 'sessionId'), extract(file, '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})')),
    -- The directory under ~/.claude/projects: the session's working
    -- directory with / and . replaced by -.
    project LowCardinality(String) MATERIALIZED extract(file, '/projects/([^/]+)/'),
    -- Metadata records (titles, modes, ...) have no timestamp: 1970-01-01.
    ts DateTime64(3, 'UTC') MATERIALIZED parseDateTime64BestEffortOrZero(JSONExtractString(raw, 'timestamp'), 3, 'UTC'),
    uuid String MATERIALIZED JSONExtractString(raw, 'uuid'),
    parent_uuid String MATERIALIZED JSONExtractString(raw, 'parentUuid'),
    is_sidechain Bool MATERIALIZED JSONExtractBool(raw, 'isSidechain'),
    cwd String MATERIALIZED JSONExtractString(raw, 'cwd'),
    git_branch String MATERIALIZED JSONExtractString(raw, 'gitBranch'),
    version LowCardinality(String) MATERIALIZED JSONExtractString(raw, 'version'),
    entrypoint LowCardinality(String) MATERIALIZED JSONExtractString(raw, 'entrypoint'),

    -- Assistant records. Claude Code writes one record per content block of
    -- a response, each with the response's whole usage: sum tokens per
    -- message_id (the claude.api_calls view does), not per row.
    message_id String MATERIALIZED JSONExtractString(raw, 'message', 'id'),
    request_id String MATERIALIZED JSONExtractString(raw, 'requestId'),
    model LowCardinality(String) MATERIALIZED JSONExtractString(raw, 'message', 'model'),
    input_tokens UInt64 MATERIALIZED JSONExtractUInt(raw, 'message', 'usage', 'input_tokens'),
    output_tokens UInt64 MATERIALIZED JSONExtractUInt(raw, 'message', 'usage', 'output_tokens'),
    cache_creation_tokens UInt64 MATERIALIZED JSONExtractUInt(raw, 'message', 'usage', 'cache_creation_input_tokens'),
    cache_read_tokens UInt64 MATERIALIZED JSONExtractUInt(raw, 'message', 'usage', 'cache_read_input_tokens'),
    tool_names Array(LowCardinality(String)) MATERIALIZED arrayFilter(name -> name != '', arrayMap(block -> if(JSONExtractString(block, 'type') = 'tool_use', JSONExtractString(block, 'name'), ''), JSONExtractArrayRaw(raw, 'message', 'content'))),

    -- The words of a user or assistant message, for search: what was typed,
    -- answered and thought, tool calls (name and input) and tool results.
    text String MATERIALIZED if(
        JSONType(raw, 'message', 'content') = 'String',
        JSONExtractString(raw, 'message', 'content'),
        arrayStringConcat(arrayMap(block -> multiIf(
            JSONExtractString(block, 'type') = 'text', JSONExtractString(block, 'text'),
            JSONExtractString(block, 'type') = 'thinking', JSONExtractString(block, 'thinking'),
            JSONExtractString(block, 'type') = 'tool_use', concat(JSONExtractString(block, 'name'), ' ', JSONExtractRaw(block, 'input')),
            JSONExtractString(block, 'type') = 'tool_result', if(
                JSONType(block, 'content') = 'String',
                JSONExtractString(block, 'content'),
                arrayStringConcat(arrayMap(part -> JSONExtractString(part, 'text'), JSONExtractArrayRaw(block, 'content')), '\n')),
            ''), JSONExtractArrayRaw(raw, 'message', 'content')), '\n')
    ) CODEC(ZSTD(3)),

    -- Vector may send a line again (a retry, a checkpoint lost): the same
    -- line hashes the same, and merges keep one copy. Queries that must not
    -- count a copy twice before the merge use FINAL.
    line_hash UInt64 MATERIALIZED cityHash64(raw),
    inserted_at DateTime DEFAULT now(),

    INDEX text_search text TYPE text(tokenizer = splitByNonAlpha)
)
ENGINE = ReplacingMergeTree
ORDER BY (session_id, ts, line_hash);

-- One row per API response (assistant message), with its token usage once.
CREATE VIEW IF NOT EXISTS claude.api_calls AS
SELECT
    e.message_id AS message_id,
    any(e.host) AS host,
    any(e.session_id) AS session_id,
    any(e.project) AS project,
    min(e.ts) AS ts,
    any(e.model) AS model,
    any(e.is_sidechain) AS is_sidechain,
    max(e.input_tokens) AS input_tokens,
    max(e.output_tokens) AS output_tokens,
    max(e.cache_creation_tokens) AS cache_creation_tokens,
    max(e.cache_read_tokens) AS cache_read_tokens,
    arrayDistinct(arrayFlatten(groupArray(e.tool_names))) AS tool_names
FROM claude.events AS e
WHERE e.type = 'assistant' AND e.message_id != '' AND e.model != '<synthetic>'
GROUP BY e.message_id;

-- One row per session.
CREATE VIEW IF NOT EXISTS claude.sessions AS
SELECT
    e.session_id AS session_id,
    any(e.host) AS host,
    any(e.project) AS project,
    anyIf(e.cwd, e.cwd != '') AS cwd,
    minIf(e.ts, e.ts > 0) AS started,
    maxIf(e.ts, e.ts > 0) AS ended,
    -- The title the user gave, else the one Claude Code generated.
    coalesce(
        nullIf(argMaxIf(JSONExtractString(e.raw, 'customTitle'), e.inserted_at, e.type = 'custom-title'), ''),
        nullIf(argMaxIf(JSONExtractString(e.raw, 'aiTitle'), e.inserted_at, e.type = 'ai-title'), '')) AS title,
    countIf(e.type = 'user' AND NOT e.is_sidechain AND JSONType(e.raw, 'message', 'content') = 'String') AS prompts,
    uniqExactIf(e.message_id, e.type = 'assistant' AND e.message_id != '') AS responses,
    groupUniqArrayIf(e.model, e.model != '' AND e.model != '<synthetic>') AS models,
    groupUniqArrayIf(e.git_branch, e.git_branch != '') AS branches
FROM claude.events AS e
GROUP BY e.session_id;
