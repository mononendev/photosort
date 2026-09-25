FROM node:22-alpine AS dev
WORKDIR /app
RUN npm install -g pnpm@9.15.0
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store --package-import-method copy
COPY web/ .
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]

FROM node:22-alpine AS builder
WORKDIR /app
RUN npm install -g pnpm@9.15.0
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store --package-import-method copy
COPY web/ .
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BUILD_TIME=
ENV VITE_APP_VERSION=${VERSION} VITE_GIT_SHA=${GIT_SHA} VITE_BUILD_TIME=${BUILD_TIME}
RUN npm run build

FROM nginx:alpine AS production
COPY --from=builder /app/dist /usr/share/nginx/html
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
