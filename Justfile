# 2 is something

set dotenv-load
set export
set shell := ["bash", "-uc"]
set windows-shell := ["powershell.exe", "-NoLogo", "-Command"]

project := file_name(justfile_directory())
version := `git describe --tags --always 2>/dev/null || echo "dev"`

# =============================================================================
# Default
# =============================================================================

default:
    @just --list --unsorted

# =============================================================================
# MongoDB
# =============================================================================

[group("db")]
mongo-up *ARGS:
    docker compose -f mongo.yml up {{ARGS}}

[group("db")]
mongo *ARGS:
    docker compose -f mongo.yml up -d {{ARGS}}

[group("db")]
mongo-down *ARGS:
    docker compose -f mongo.yml down {{ARGS}}

[group("db")]
mongo-stop:
    docker compose -f mongo.yml stop

[group("db")]
mongo-build *ARGS:
    docker compose -f mongo.yml build {{ARGS}}

[group("db")]
mongo-logs:
    docker compose -f mongo.yml logs -f
