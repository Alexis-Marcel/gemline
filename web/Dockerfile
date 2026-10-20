# syntax=docker/dockerfile:1

# Frontend image: Vite production build → Caddy alpine serving the dist/
# Caddy handles SPA routing (fallback to index.html for /game/<id> etc.)
# and gzip/brotli compression. Resulting size: ~25 MB.

FROM node:25-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# No build-time configuration: the frontend reads the host's account method
# from /api/config, so one image serves every deployment.
RUN npm run build

FROM caddy:2-alpine
COPY --from=build /src/dist /srv
COPY Caddyfile /etc/caddy/Caddyfile
EXPOSE 8080
