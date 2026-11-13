# node:24.22.3-bookworm-slim
FROM node@sha256:dc26ae92c9fb15cf6b1d63e20f0944b2efe0f115b20e00be48fb23fceb061c54 AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm build && pnpm prune --prod

# node:24.22.3-bookworm-slim
FROM node@sha256:dc26ae92c9fb15cf6b1d63e20f0944b2efe0f115b20e00be48fb23fceb061c54
LABEL org.opencontainers.image.source="https://github.com/davcs86/commsgun" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /app
COPY --from=build /src/build ./build
COPY --from=build /src/node_modules ./node_modules
COPY --from=build /src/package.json ./package.json
USER 10001:10001
ENV NODE_ENV=production PORT=9202 HOST=0.0.0.0 TZ=UTC \
    PROTOCOL_HEADER=x-forwarded-proto HOST_HEADER=x-forwarded-host \
    ADDRESS_HEADER=x-forwarded-for XFF_DEPTH=1
EXPOSE 9202
CMD ["node","build"]
