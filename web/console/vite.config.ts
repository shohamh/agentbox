import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', assetsDir: 'assets', sourcemap: false },
  server: { proxy: { '/api': 'http://localhost:18080', '/auth': 'http://localhost:18080' } },
})
