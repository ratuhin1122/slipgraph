FROM node:24-alpine AS build
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY api /src/api
COPY web/ ./
RUN npm run build
FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY web/nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 8080
