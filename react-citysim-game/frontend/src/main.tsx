import React from 'react';
import { createRoot } from 'react-dom/client';

const Game: React.FC = () => {
  return <div>Game</div>;
};

createRoot(document.getElementById('root')!).render(<Game />);
