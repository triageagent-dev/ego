# ego UI

React + TypeScript frontend for ego, built with Vite.

## Stack

- **React 19** with TypeScript
- **Ant Design** for tables and form controls
- **Emotion** (`@emotion/react`) for CSS-in-JS
- **Lucide** icons, **Framer Motion** for animations
- Dark theme with CSS variables defined in `src/index.css`

## Development

```bash
npm install
npm run dev          # Vite dev server on http://localhost:5173
```

The dev server calls the backend at `http://localhost:8001` directly (CORS, no proxy). Run the backend in a separate terminal:

```bash
# from repo root
make dev-backend
```

## Building

```bash
npm run build        # production build → dist/
npx tsc --noEmit     # type check only
```

The `dist/` output is embedded into the Go binary (`ui/embed.go`).
