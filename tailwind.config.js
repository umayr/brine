module.exports = {
  content: [
    './internal/web/templates/**/*.html',
    './internal/web/static/**/*.js',
  ],
  daisyui: {
    themes: ['night', 'light'],
  },
  plugins: [
    require('daisyui'),
  ],
};
