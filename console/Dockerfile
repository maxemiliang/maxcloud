# The console's image: the Nuxt server, built here and run as a plain Node
# process. .github/workflows/console-image.yaml builds and pushes it.
FROM node:24.21.0-alpine AS build
WORKDIR /src
ENV NUXT_TELEMETRY_DISABLED=1
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# Nitro's output carries everything it needs: no node_modules, no npm.
FROM node:24.21.0-alpine
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=8080
WORKDIR /app
COPY --from=build /src/.output ./.output
# A numeric user, so Pod Security `restricted` (runAsNonRoot) can tell.
USER 65532:65532
EXPOSE 8080
CMD ["node", ".output/server/index.mjs"]
