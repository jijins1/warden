# Build stage
FROM node:20-alpine AS build

WORKDIR /app

# Copy package files
COPY package.json package-lock.json* ./

# Install dependencies
RUN npm ci --only=production --no-audit --no-fund

# Copy source files
COPY . .

# Build the application
RUN npm run build -- --configuration production --output-path /dist

# Production stage
FROM nginx:alpine

# Copy nginx configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Copy built files
COPY --from=build /app/dist/warden-ui /usr/share/nginx/html

# Copy static assets if any
COPY --from=build /app/public /usr/share/nginx/html/assets

# Set environment variables
ENV NG_APP_API_URL=/api
ENV NG_APP_WS_URL=ws://localhost

# Expose port
EXPOSE 80

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost/ || exit 1

# Start nginx
CMD ["nginx", "-g", "daemon off;"]
