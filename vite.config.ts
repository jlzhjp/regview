import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  base: '/assets/frontend/',
  plugins: [tailwindcss()],
  publicDir: false,
  build: {
    outDir: 'internal/assets/dist/frontend',
    emptyOutDir: true,
    rolldownOptions: {
      input: 'frontend/src/app.ts',
      output: {
        entryFileNames: 'app.js',
        assetFileNames: '[name][extname]',
      },
    },
  },
});
