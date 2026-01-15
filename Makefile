# AngelaMos | 2026
# Makefile - CertGamesDB Argos

.PHONY: help mongo-up mongo-down mongo-restart mongo-status mongo-logs app-up app-down app-restart app-logs app-build all-up all-down

help:
	@echo "MongoDB Replica Set Commands:"
	@echo "  make mongo-up        - Start MongoDB replica set + mongo-express"
	@echo "  make mongo-down      - Stop MongoDB replica set"
	@echo "  make mongo-restart   - Restart MongoDB replica set"
	@echo "  make mongo-status    - Show replica set status"
	@echo "  make mongo-logs      - Tail MongoDB primary logs"
	@echo ""
	@echo "Application Commands:"
	@echo "  make app-up          - Start backend + frontend containers"
	@echo "  make app-down        - Stop backend + frontend containers"
	@echo "  make app-restart     - Restart backend + frontend containers"
	@echo "  make app-build       - Rebuild and start app containers"
	@echo "  make app-logs        - Tail backend logs"
	@echo ""
	@echo "Combined Commands:"
	@echo "  make all-up          - Start everything (mongo + app)"
	@echo "  make all-down        - Stop everything"

# MongoDB Replica Set
mongo-up:
	docker compose -f mongo-rs.yml up -d

mongo-down:
	docker compose -f mongo-rs.yml down

mongo-restart:
	docker compose -f mongo-rs.yml restart

mongo-status:
	@./scripts/mongo-status.sh

mongo-logs:
	docker logs -f mongodb_primary --tail 100

# Application (Backend + Frontend)
app-up:
	docker compose -f dev.compose.yml up -d

app-down:
	docker compose -f dev.compose.yml down

app-restart:
	docker compose -f dev.compose.yml restart

app-build:
	docker compose -f dev.compose.yml up -d --build

app-logs:
	docker logs -f oneisnun_backend --tail 100

# Combined
all-up: mongo-up app-up

all-down: app-down mongo-down
