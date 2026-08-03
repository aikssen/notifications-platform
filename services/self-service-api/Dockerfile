# syntax=docker/dockerfile:1
#
# Build context is the repository root, because this is a pnpm workspace:
#   docker build -f services/self-service-api/Dockerfile .

FROM node:22-alpine AS base
RUN corepack enable
WORKDIR /app
ENV PNPM_HOME=/pnpm
ENV PATH="$PNPM_HOME:$PATH"

# Manifests first, so a source change does not reinstall the dependency tree.
FROM base AS manifests
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml tsconfig.base.json ./
COPY services/self-service-api/package.json ./services/self-service-api/

FROM manifests AS build
RUN pnpm install --frozen-lockfile --filter self-service-api...
COPY services/self-service-api ./services/self-service-api
RUN pnpm --filter self-service-api build

# Production dependencies only, hoisted so the tree can be copied as-is
# instead of as a graph of symlinks into pnpm's store.
FROM manifests AS prod-deps
RUN pnpm install --frozen-lockfile --prod --node-linker=hoisted --filter self-service-api...

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production

COPY --from=prod-deps /app/node_modules ./node_modules
COPY --from=build /app/services/self-service-api/dist ./dist
COPY --from=build /app/services/self-service-api/package.json ./package.json

USER node
EXPOSE 3002
CMD ["node", "dist/main.js"]
