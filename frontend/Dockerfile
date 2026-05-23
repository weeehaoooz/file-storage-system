# --- Stage 1: Build Angular application ---
FROM node:22-alpine AS builder

WORKDIR /app

# Copy dependency manifests
COPY package.json package-lock.json ./

# Install npm dependencies
RUN npm ci

# Copy the rest of the frontend source code
COPY . .

# Build the production Angular project
RUN npm run build -- --configuration=production

# --- Stage 2: Serve the application using Nginx ---
FROM nginx:1.27-alpine

# Copy built browser bundle to Nginx standard HTML root
COPY --from=builder /app/dist/frontend/browser /usr/share/nginx/html

# Overwrite Nginx configuration
COPY nginx.conf /etc/nginx/nginx.conf

# Expose HTTP port
EXPOSE 80

# Start Nginx
CMD ["nginx", "-g", "daemon off;"]
