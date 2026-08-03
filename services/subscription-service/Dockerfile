# syntax=docker/dockerfile:1
#
# Build context is the repository root, because this is a pnpm workspace:
#   docker build -f services/subscription-service/Dockerfile .

FROM node:22-alpine AS base
RUN corepack enable
WORKDIR /app
ENV PNPM_HOME=/pnpm
ENV PATH="$PNPM_HOME:$PATH"

# Manifests first, so a source change does not reinstall the dependency tree.
FROM base AS manifests
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml tsconfig.base.json ./
COPY services/subscription-service/package.json ./services/subscription-service/

FROM manifests AS build
RUN pnpm install --frozen-lockfile --filter subscription-service...
COPY services/subscription-service ./services/subscription-service
RUN pnpm --filter subscription-service build

# Production dependencies only, hoisted so the tree can be copied as-is
# instead of as a graph of symlinks into pnpm's store.
FROM manifests AS prod-deps
RUN pnpm install --frozen-lockfile --prod --node-linker=hoisted --filter subscription-service...

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production

COPY --from=prod-deps /app/node_modules ./node_modules
COPY --from=build /app/services/subscription-service/dist ./dist
COPY --from=build /app/services/subscription-service/package.json ./package.json

USER node
EXPOSE 3001
CMD ["node", "dist/main.js"]
