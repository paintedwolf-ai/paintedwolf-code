---
name: inspect-a-database
description: Inspect SQLite files or query PostgreSQL, MySQL/MariaDB, MongoDB, and Redis safely for schema, data, and cache evidence.
metadata:
  paintedwolf.template_resources: references/inspect_a_sqlite_database.md|references/query_a_postgres_database.md|references/query_a_mysql_database.md|references/query_a_mongodb_database.md|references/inspect_a_redis_instance.md
  host_resources: mongodb-shell|mysql-cli|postgresql-cli|redis-cli|sqlite
---

# Inspect a database

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Inspect a sqlite database](references/inspect_a_sqlite_database.md) — Answer schema and data questions about a SQLite database file with the sqlite3 CLI when inspecting a .db/.sqlite file, local app state, or a test fixture.
- [Query a postgres database](references/query_a_postgres_database.md) — Answer schema and data questions against PostgreSQL with psql under read-only discipline when inspecting tables, data quality, or query behavior.
- [Query a mysql database](references/query_a_mysql_database.md) — Answer schema and data questions against MySQL or MariaDB with the mysql client under read-only discipline when inspecting tables, data quality, or query behavior.
- [Query a mongodb database](references/query_a_mongodb_database.md) — Answer schema and data questions against MongoDB with mongosh when inspecting collection validators, sampled document shapes, indexes, or query behavior.
- [Inspect a redis instance](references/inspect_a_redis_instance.md) — Answer cache questions about Redis keys, memory, TTLs, hit rates, and evictions with redis-cli under production-safe reads.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
