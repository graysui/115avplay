/** Tailwind config for the single-file admin console (web/dist/index.html).
 *  Regenerate the inlined CSS with:
 *    npx tailwindcss@3.4.1 -c web/tailwind.config.js -i web/src/tailwind.input.css -o /tmp/tw.css --minify
 *  then paste the output into the <style> block of web/dist/index.html.
 */
module.exports = {
  darkMode: 'class',
  content: ['./dist/index.html'],
  theme: {
    extend: {
      colors: {
        brand: {
          50: '#f0f9ff',
          100: '#e0f2fe',
          500: '#0ea5e9',
          600: '#0284c7',
          700: '#0369a1',
          900: '#0c4a6e',
        },
      },
    },
  },
  plugins: [],
}
