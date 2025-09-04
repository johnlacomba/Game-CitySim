# CitySim Prototype

Multiplayer isometric city-building simulation (prototype) using Go backend and React + Canvas frontend. Server is authoritative over game state.

## Features Implemented
- Authoritative Go server maintaining map, zoning, construction ticks
- WebSocket real-time updates (full state snapshot + tick + zone placed events)
- Simple demand model and probabilistic construction progress
- React canvas isometric renderer with basic terrain (grass, water, hill, forest)
- Player zoning tools (R, C, I) with cost deduction on server

## Next Steps / Ideas
- Persist game state (e.g., BoltDB / SQLite)
- Authentication & player reconnect
- Better demand simulation (population/jobs/goods feedback loops)
- Building variety sprites & animation
- Fog of war / per-player visibility
- Performance: delta state updates (chunks) instead of full arrays
- Pathfinding for agents (citizens, freight)
- Economic simulation (taxes, expenses)
- UI for player money & ownership overlays

## Running Backend
Requires Go 1.22+

```
cd backend
go mod tidy
go run .
```
Server listens on :8080

## Running Frontend
Requires Node 18+

```
cd frontend
npm install
npm run dev
```
Open the printed Vite dev URL (usually http://localhost:5173) – it will connect to ws://localhost:8080.

## Protocol (Initial)
Events from server:
- full_state: entire `GameState`
- tick: `{ tick, demand }`
- zone_placed: `{ x, y, zone }`

Client actions:
- place_zone: `{ x, y, zone }`

## Data Shapes (Simplified)
See `backend/main.go` & `frontend/src/ws.ts`.

## Disclaimer
Prototype quality. Not optimized, not secure. For experimentation only.
