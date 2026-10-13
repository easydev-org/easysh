import path from 'node:path'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// Same stack and build target as the worker's control panel: Svelte 5 +
// Tailwind 4, built flat into internal/webui/dist (embedded via go:embed).
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: { $lib: path.resolve('./src/lib') },
  },
  server: {
    host: '0.0.0.0',
    port: 18501,
    strictPort: true,
    allowedHosts: true,
    proxy: { '/api': 'http://localhost:18080' },
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    target: 'es2022',
  },
})
