# syntax=docker/dockerfile:1

# Frontend image: Vite production build → Caddy alpine serving the dist/
# Caddy handles SPA routing (fallback to index.html for /game/<id> etc.)
# and gzip/brotli compression. Resulting size: ~25 MB.

FROM node:25-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# Frontend env vars must be available at build time so Vite inlines them
# into the JS bundle. They have the VITE_ prefix and are PUBLIC by design.
ARG VITE_SUPABASE_URL
ARG VITE_SUPABASE_PUBLISHABLE_KEY
ENV VITE_SUPABASE_URL=${VITE_SUPABASE_URL}
ENV VITE_SUPABASE_PUBLISHABLE_KEY=${VITE_SUPABASE_PUBLISHABLE_KEY}
RUN npm run build

FROM caddy:2-alpine
COPY --from=build /src/dist /srv
COPY Caddyfile /etc/caddy/Caddyfile
EXPOSE 8080
