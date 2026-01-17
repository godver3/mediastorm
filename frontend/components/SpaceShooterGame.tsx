import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Dimensions,
  Modal,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
  type GestureResponderEvent,
} from 'react-native';

// Game constants
const GAME_WIDTH = 30;
const GAME_HEIGHT = 20;
const PLAYER_CHAR = '^';
const BULLET_CHAR = '|';
const STAR_CHAR = '.';
const GAME_SPEED = 80; // ms per frame
const STARTING_LIVES = 3;
const COMBO_TIMEOUT = 1500; // ms to maintain combo
const BOSS_SPAWN_WAVE = 3; // Boss spawns every X waves

// Enemy types with different behaviors
type EnemyType = 'basic' | 'zigzag' | 'fast' | 'tank';

interface EnemyConfig {
  char: string;
  hp: number;
  speed: number;
  points: number;
  zigzag?: boolean;
}

const ENEMY_TYPES: Record<EnemyType, EnemyConfig> = {
  basic: { char: 'W', hp: 1, speed: 2, points: 10 },
  zigzag: { char: 'M', hp: 1, speed: 2, points: 15, zigzag: true },
  fast: { char: 'V', hp: 1, speed: 1, points: 20 },
  tank: { char: '@', hp: 3, speed: 3, points: 50 },
};

const EXPLOSION_CHARS = ['*', '+', 'o', '.'];

// Boss ASCII art frames - animates between these
const BOSS_FRAMES = [
  ['  /-===-\\  ', ' |[o]_[o]| ', ' |<#####>| ', ' |_/\\_/\\_| ', '  \\-====/  '],
  ['  \\-===-/  ', ' |[o]_[o]| ', ' |>#####<| ', ' |_\\/\\_\\/| ', '  /-====\\  '],
];

// Boss damage states - parts break off
const BOSS_DAMAGE_CHARS = [
  {
    threshold: 0.75,
    replacements: [
      [0, 2, ' '],
      [0, 8, ' '],
      [4, 2, ' '],
      [4, 8, ' '],
    ],
  },
  {
    threshold: 0.5,
    replacements: [
      [1, 1, '.'],
      [1, 9, '.'],
      [3, 1, ' '],
      [3, 9, ' '],
    ],
  },
  {
    threshold: 0.25,
    replacements: [
      [2, 1, '*'],
      [2, 9, '*'],
      [1, 5, 'X'],
      [3, 3, ' '],
      [3, 7, ' '],
    ],
  },
];

const BOSS_WIDTH = 11;
const BOSS_HEIGHT = 5;
const BOSS_MAX_HP = 30;
const BOSS_POINTS = 500;

interface Position {
  x: number;
  y: number;
}

interface Bullet extends Position {
  id: number;
}

interface Enemy extends Position {
  id: number;
  type: EnemyType;
  hp: number;
  moveCounter: number;
  zigzagDir: number;
}

interface Boss {
  x: number;
  y: number;
  hp: number;
  maxHp: number;
  moveCounter: number;
  animFrame: number;
  direction: number; // 1 or -1 for horizontal movement
  active: boolean;
}

interface Explosion extends Position {
  id: number;
  frame: number;
}

interface Star extends Position {
  speed: number;
}

interface SpaceShooterGameProps {
  visible: boolean;
  onClose: () => void;
}

export function SpaceShooterGame({ visible, onClose }: SpaceShooterGameProps) {
  const [playerX, setPlayerX] = useState(Math.floor(GAME_WIDTH / 2));
  const [bullets, setBullets] = useState<Bullet[]>([]);
  const [enemies, setEnemies] = useState<Enemy[]>([]);
  const [explosions, setExplosions] = useState<Explosion[]>([]);
  const [stars, setStars] = useState<Star[]>([]);
  const [score, setScore] = useState(0);
  const [lives, setLives] = useState(STARTING_LIVES);
  const [combo, setCombo] = useState(0);
  const [comboMultiplier, setComboMultiplier] = useState(1);
  const [gameOver, setGameOver] = useState(false);
  const [highScore, setHighScore] = useState(0);
  const [wave, setWave] = useState(1);
  const [showDamage, setShowDamage] = useState(false);
  const [boss, setBoss] = useState<Boss | null>(null);
  const [bossesDefeated, setBossesDefeated] = useState(0);

  const bulletIdRef = useRef(0);
  const enemyIdRef = useRef(0);
  const explosionIdRef = useRef(0);
  const gameLoopRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const touchStartRef = useRef<{ x: number; y: number; playerX: number } | null>(null);
  const lastShotTime = useRef(0);
  const lastComboTime = useRef(0);
  const frameCountRef = useRef(0);
  const playerXRef = useRef(playerX);
  const scoreRef = useRef(score);
  const livesRef = useRef(lives);
  const comboRef = useRef(combo);

  // Keep refs in sync with state
  useEffect(() => {
    playerXRef.current = playerX;
  }, [playerX]);

  useEffect(() => {
    scoreRef.current = score;
    const newWave = Math.floor(score / 75) + 1; // Wave every ~7-8 kills
    if (newWave !== wave) {
      setWave(newWave);
    }
  }, [score, wave]);

  useEffect(() => {
    livesRef.current = lives;
  }, [lives]);

  useEffect(() => {
    comboRef.current = combo;
    if (combo >= 10) setComboMultiplier(4);
    else if (combo >= 5) setComboMultiplier(3);
    else if (combo >= 3) setComboMultiplier(2);
    else setComboMultiplier(1);
  }, [combo]);

  // Initialize stars
  useEffect(() => {
    const initialStars: Star[] = [];
    for (let i = 0; i < 15; i++) {
      initialStars.push({
        x: Math.floor(Math.random() * GAME_WIDTH),
        y: Math.floor(Math.random() * GAME_HEIGHT),
        speed: Math.random() > 0.5 ? 1 : 2,
      });
    }
    setStars(initialStars);
  }, []);

  // Reset game
  const resetGame = useCallback(() => {
    setPlayerX(Math.floor(GAME_WIDTH / 2));
    setBullets([]);
    setEnemies([]);
    setExplosions([]);
    setScore(0);
    setLives(STARTING_LIVES);
    setCombo(0);
    setComboMultiplier(1);
    setWave(1);
    setGameOver(false);
    setBoss(null);
    setBossesDefeated(0);
    bulletIdRef.current = 0;
    enemyIdRef.current = 0;
    explosionIdRef.current = 0;
    frameCountRef.current = 0;
  }, []);

  // Spawn boss
  const spawnBoss = useCallback(() => {
    setBoss({
      x: Math.floor((GAME_WIDTH - BOSS_WIDTH) / 2),
      y: 0,
      hp: BOSS_MAX_HP,
      maxHp: BOSS_MAX_HP,
      moveCounter: 0,
      animFrame: 0,
      direction: 1,
      active: true,
    });
  }, []);

  // Shoot bullet
  const shoot = useCallback(() => {
    const now = Date.now();
    if (now - lastShotTime.current < 120) return;
    lastShotTime.current = now;

    setBullets((prev) => [...prev, { id: bulletIdRef.current++, x: playerXRef.current, y: GAME_HEIGHT - 2 }]);
  }, []);

  // Spawn enemy
  const spawnEnemy = useCallback((currentWave: number) => {
    let type: EnemyType = 'basic';
    const rand = Math.random();

    if (currentWave >= 5 && rand < 0.15) {
      type = 'tank';
    } else if (currentWave >= 3 && rand < 0.3) {
      type = 'fast';
    } else if (currentWave >= 2 && rand < 0.4) {
      type = 'zigzag';
    }

    const config = ENEMY_TYPES[type];
    const newEnemy: Enemy = {
      id: enemyIdRef.current++,
      x: Math.floor(Math.random() * (GAME_WIDTH - 4)) + 2,
      y: 0,
      type,
      hp: config.hp,
      moveCounter: 0,
      zigzagDir: Math.random() > 0.5 ? 1 : -1,
    };

    setEnemies((prev) => [...prev, newEnemy]);
  }, []);

  // Get boss ASCII art with damage applied
  const getBossArt = useCallback((bossState: Boss): string[] => {
    const frame = BOSS_FRAMES[bossState.animFrame % BOSS_FRAMES.length];
    const art = frame.map((row) => row.split(''));

    const hpPercent = bossState.hp / bossState.maxHp;

    // Apply damage effects
    for (const damageState of BOSS_DAMAGE_CHARS) {
      if (hpPercent <= damageState.threshold) {
        for (const [row, col, char] of damageState.replacements) {
          if (art[row as number] && art[row as number][col as number] !== undefined) {
            art[row as number][col as number] = char as string;
          }
        }
      }
    }

    // Add random sparks/damage when low HP
    if (hpPercent < 0.5) {
      const sparkChars = ['*', '+', 'x', '.'];
      for (let i = 0; i < Math.floor((1 - hpPercent) * 5); i++) {
        const row = Math.floor(Math.random() * BOSS_HEIGHT);
        const col = Math.floor(Math.random() * BOSS_WIDTH);
        if (art[row] && art[row][col] && art[row][col] !== ' ') {
          if (Math.random() > 0.7) {
            art[row][col] = sparkChars[Math.floor(Math.random() * sparkChars.length)];
          }
        }
      }
    }

    return art.map((row) => row.join(''));
  }, []);

  // Game loop
  useEffect(() => {
    if (!visible || gameOver) {
      if (gameLoopRef.current) {
        clearTimeout(gameLoopRef.current);
        gameLoopRef.current = null;
      }
      return;
    }

    const runGameLoop = () => {
      frameCountRef.current++;
      const frame = frameCountRef.current;
      const currentWave = Math.floor(scoreRef.current / 75) + 1;

      // Check combo timeout
      if (comboRef.current > 0 && Date.now() - lastComboTime.current > COMBO_TIMEOUT) {
        setCombo(0);
      }

      // Move bullets
      setBullets((prev) => prev.map((b) => ({ ...b, y: b.y - 1 })).filter((b) => b.y >= 0));

      // Update boss
      setBoss((prevBoss) => {
        if (!prevBoss || !prevBoss.active) return prevBoss;

        const newBoss = { ...prevBoss };
        newBoss.moveCounter++;

        // Animate every 10 frames
        if (newBoss.moveCounter % 10 === 0) {
          newBoss.animFrame++;
        }

        // Move horizontally every 5 frames
        if (newBoss.moveCounter % 5 === 0) {
          newBoss.x += newBoss.direction;
          if (newBoss.x <= 0 || newBoss.x + BOSS_WIDTH >= GAME_WIDTH) {
            newBoss.direction *= -1;
          }
        }

        // Move down slowly every 30 frames
        if (newBoss.moveCounter % 30 === 0) {
          newBoss.y++;

          // Boss reached bottom
          if (newBoss.y + BOSS_HEIGHT >= GAME_HEIGHT) {
            setTimeout(() => {
              if (livesRef.current > 0) {
                setLives((l) => {
                  const newLives = l - 2; // Boss does 2 damage
                  if (newLives <= 0) {
                    setGameOver(true);
                    setHighScore((h) => Math.max(h, scoreRef.current));
                  }
                  return Math.max(0, newLives);
                });
                setCombo(0);
                setShowDamage(true);
                setTimeout(() => setShowDamage(false), 150);
              }
            }, 0);
            return null;
          }
        }

        return newBoss;
      });

      // Check if boss should spawn (every BOSS_SPAWN_WAVE waves, based on bosses already defeated)
      setBoss((prevBoss) => {
        if (prevBoss === null) {
          setBossesDefeated((defeated) => {
            const expectedBosses = Math.floor(currentWave / BOSS_SPAWN_WAVE);
            if (expectedBosses > defeated) {
              // Spawn boss
              setTimeout(() => spawnBoss(), 0);
            }
            return defeated;
          });
        }
        return prevBoss;
      });

      // Move enemies
      setEnemies((prev) => {
        const updatedEnemies: Enemy[] = [];
        let lostLife = false;

        for (const enemy of prev) {
          const config = ENEMY_TYPES[enemy.type];
          const newEnemy = { ...enemy, moveCounter: enemy.moveCounter + 1 };

          if (newEnemy.moveCounter >= config.speed) {
            newEnemy.moveCounter = 0;
            newEnemy.y += 1;

            if (config.zigzag) {
              newEnemy.x += newEnemy.zigzagDir;
              if (newEnemy.x <= 1 || newEnemy.x >= GAME_WIDTH - 2) {
                newEnemy.zigzagDir *= -1;
              }
            }
          }

          if (newEnemy.y >= GAME_HEIGHT) {
            if (!lostLife) {
              lostLife = true;
              setTimeout(() => {
                if (livesRef.current > 0) {
                  setLives((l) => {
                    const newLives = l - 1;
                    if (newLives <= 0) {
                      setGameOver(true);
                      setHighScore((h) => Math.max(h, scoreRef.current));
                    }
                    return newLives;
                  });
                  setCombo(0);
                  setShowDamage(true);
                  setTimeout(() => setShowDamage(false), 150);
                }
              }, 0);
            }
            continue;
          }

          if (newEnemy.y >= GAME_HEIGHT - 1 && Math.abs(newEnemy.x - playerXRef.current) <= 1) {
            if (!lostLife) {
              lostLife = true;
              setTimeout(() => {
                if (livesRef.current > 0) {
                  setLives((l) => {
                    const newLives = l - 1;
                    if (newLives <= 0) {
                      setGameOver(true);
                      setHighScore((h) => Math.max(h, scoreRef.current));
                    }
                    return newLives;
                  });
                  setCombo(0);
                  setShowDamage(true);
                  setTimeout(() => setShowDamage(false), 150);
                }
              }, 0);
            }
            continue;
          }

          updatedEnemies.push(newEnemy);
        }

        return updatedEnemies;
      });

      // Move stars
      setStars((prev) =>
        prev.map((s) => ({
          ...s,
          y: frame % s.speed === 0 ? (s.y + 1) % GAME_HEIGHT : s.y,
        })),
      );

      // Update explosions
      setExplosions((prev) =>
        prev.map((e) => ({ ...e, frame: e.frame + 1 })).filter((e) => e.frame < EXPLOSION_CHARS.length),
      );

      // Spawn enemies (reduced when boss is active)
      setBoss((currentBoss) => {
        const spawnRate = currentBoss ? 40 : Math.max(10, 25 - currentWave * 2);
        if (frame % spawnRate === 0) {
          const enemiesToSpawn = currentBoss ? 1 : currentWave >= 5 ? 2 : 1;
          for (let i = 0; i < enemiesToSpawn; i++) {
            if (Math.random() > 0.2) {
              spawnEnemy(currentWave);
            }
          }
        }
        return currentBoss;
      });

      // Check bullet collisions with boss
      setBullets((prevBullets) => {
        const bulletsToRemove = new Set<number>();

        setBoss((prevBoss) => {
          if (!prevBoss || !prevBoss.active) return prevBoss;

          const newBoss = { ...prevBoss };

          for (const bullet of prevBullets) {
            if (bulletsToRemove.has(bullet.id)) continue;

            // Check if bullet hits boss bounds
            if (
              bullet.x >= newBoss.x &&
              bullet.x < newBoss.x + BOSS_WIDTH &&
              bullet.y >= newBoss.y &&
              bullet.y < newBoss.y + BOSS_HEIGHT
            ) {
              bulletsToRemove.add(bullet.id);
              newBoss.hp--;

              // Add small explosion at hit point
              setExplosions((prev) => [...prev, { id: explosionIdRef.current++, x: bullet.x, y: bullet.y, frame: 0 }]);

              // Boss defeated
              if (newBoss.hp <= 0) {
                // Big explosion
                for (let i = 0; i < 15; i++) {
                  const expX = newBoss.x + Math.floor(Math.random() * BOSS_WIDTH);
                  const expY = newBoss.y + Math.floor(Math.random() * BOSS_HEIGHT);
                  setTimeout(() => {
                    setExplosions((prev) => [...prev, { id: explosionIdRef.current++, x: expX, y: expY, frame: 0 }]);
                  }, i * 50);
                }

                // Add flat score for boss (no multiplier from spamming hits)
                setScore((s) => s + BOSS_POINTS);

                // Mark boss as defeated
                setBossesDefeated((d) => d + 1);

                return null;
              }
            }
          }

          return newBoss;
        });

        // Check bullet collisions with enemies
        setEnemies((prevEnemies) => {
          const updatedEnemies: Enemy[] = [];

          for (const enemy of prevEnemies) {
            let enemyHit = false;

            for (const bullet of prevBullets) {
              if (bulletsToRemove.has(bullet.id)) continue;

              if (Math.abs(bullet.x - enemy.x) <= 1 && Math.abs(bullet.y - enemy.y) <= 1) {
                bulletsToRemove.add(bullet.id);
                const newHp = enemy.hp - 1;

                if (newHp <= 0) {
                  enemyHit = true;
                  const config = ENEMY_TYPES[enemy.type];

                  lastComboTime.current = Date.now();
                  setCombo((c) => c + 1);

                  const multiplier =
                    comboRef.current >= 10 ? 4 : comboRef.current >= 5 ? 3 : comboRef.current >= 3 ? 2 : 1;
                  setScore((s) => s + config.points * multiplier);

                  setExplosions((prev) => [
                    ...prev,
                    { id: explosionIdRef.current++, x: enemy.x, y: enemy.y, frame: 0 },
                  ]);
                } else {
                  updatedEnemies.push({ ...enemy, hp: newHp });
                  enemyHit = true;
                }
                break;
              }
            }

            if (!enemyHit) {
              updatedEnemies.push(enemy);
            }
          }

          return updatedEnemies;
        });

        return prevBullets.filter((b) => !bulletsToRemove.has(b.id));
      });

      // Schedule next frame
      gameLoopRef.current = setTimeout(runGameLoop, GAME_SPEED);
    };

    gameLoopRef.current = setTimeout(runGameLoop, GAME_SPEED);

    return () => {
      if (gameLoopRef.current) {
        clearTimeout(gameLoopRef.current);
        gameLoopRef.current = null;
      }
    };
  }, [visible, gameOver, spawnEnemy, spawnBoss]);

  // Render game grid
  const renderGrid = useCallback(() => {
    const grid: string[][] = Array(GAME_HEIGHT)
      .fill(null)
      .map(() => Array(GAME_WIDTH).fill(' '));

    // Draw stars
    for (const star of stars) {
      if (grid[star.y] && grid[star.y][star.x] === ' ') {
        grid[star.y][star.x] = STAR_CHAR;
      }
    }

    // Draw boss
    if (boss && boss.active) {
      const bossArt = getBossArt(boss);
      for (let row = 0; row < BOSS_HEIGHT; row++) {
        for (let col = 0; col < BOSS_WIDTH; col++) {
          const gridY = boss.y + row;
          const gridX = boss.x + col;
          if (grid[gridY] && gridX >= 0 && gridX < GAME_WIDTH && bossArt[row][col] !== ' ') {
            grid[gridY][gridX] = bossArt[row][col];
          }
        }
      }
    }

    // Draw explosions
    for (const exp of explosions) {
      if (grid[exp.y] && exp.x >= 0 && exp.x < GAME_WIDTH) {
        grid[exp.y][exp.x] = EXPLOSION_CHARS[exp.frame] || '*';
      }
    }

    // Draw bullets
    for (const bullet of bullets) {
      if (grid[bullet.y] && bullet.x >= 0 && bullet.x < GAME_WIDTH) {
        grid[bullet.y][bullet.x] = BULLET_CHAR;
      }
    }

    // Draw enemies
    for (const enemy of enemies) {
      if (grid[enemy.y] && enemy.x >= 0 && enemy.x < GAME_WIDTH) {
        const config = ENEMY_TYPES[enemy.type];
        if (enemy.type === 'tank' && enemy.hp < config.hp) {
          grid[enemy.y][enemy.x] = enemy.hp === 2 ? 'O' : 'o';
        } else {
          grid[enemy.y][enemy.x] = config.char;
        }
      }
    }

    // Draw player
    if (grid[GAME_HEIGHT - 1]) {
      grid[GAME_HEIGHT - 1][playerX] = PLAYER_CHAR;
      if (playerX > 0) grid[GAME_HEIGHT - 1][playerX - 1] = '/';
      if (playerX < GAME_WIDTH - 1) grid[GAME_HEIGHT - 1][playerX + 1] = '\\';
    }

    return grid.map((row) => row.join('')).join('\n');
  }, [stars, explosions, bullets, enemies, playerX, boss, getBossArt]);

  // Touch handlers
  const handleTouchStart = useCallback(
    (event: GestureResponderEvent) => {
      const { pageX, pageY } = event.nativeEvent;
      touchStartRef.current = { x: pageX, y: pageY, playerX };
    },
    [playerX],
  );

  const handleTouchMove = useCallback((event: GestureResponderEvent) => {
    if (!touchStartRef.current) return;

    const { pageX } = event.nativeEvent;
    const deltaX = pageX - touchStartRef.current.x;

    const screenWidth = Dimensions.get('window').width;
    const sensitivity = GAME_WIDTH / (screenWidth * 0.5);
    const gameUnits = Math.round(deltaX * sensitivity);

    const newX = Math.max(1, Math.min(GAME_WIDTH - 2, touchStartRef.current.playerX + gameUnits));
    setPlayerX(newX);
  }, []);

  const handleTouchEnd = useCallback(
    (event: GestureResponderEvent) => {
      if (!touchStartRef.current) return;

      const { pageX, pageY } = event.nativeEvent;
      const deltaX = Math.abs(pageX - touchStartRef.current.x);
      const deltaY = Math.abs(pageY - touchStartRef.current.y);

      if (deltaX < 15 && deltaY < 15) {
        shoot();
      }

      touchStartRef.current = null;
    },
    [shoot],
  );

  if (!visible) return null;

  const livesDisplay = Array(Math.max(0, lives)).fill('\u2665').join(' ');
  const emptyLives = Array(Math.max(0, STARTING_LIVES - lives))
    .fill('\u2661')
    .join(' ');

  return (
    <Modal visible={visible} transparent animationType="fade" onRequestClose={onClose}>
      <View
        style={[styles.container, showDamage && styles.damageFlash]}
        onTouchStart={handleTouchStart}
        onTouchMove={handleTouchMove}
        onTouchEnd={handleTouchEnd}>
        <View style={styles.gameContainer}>
          <View style={styles.header}>
            <Text style={styles.title}>SPACE DEFENDER</Text>
            <View style={styles.statsRow}>
              <Text style={styles.lives}>
                {livesDisplay}
                {emptyLives ? ' ' + emptyLives : ''}
              </Text>
              <Text style={styles.wave}>WAVE {wave}</Text>
            </View>
            <View style={styles.statsRow}>
              <Text style={styles.score}>SCORE: {score}</Text>
              {comboMultiplier > 1 && <Text style={styles.combo}>{comboMultiplier}x COMBO!</Text>}
            </View>
            {highScore > 0 && <Text style={styles.highScore}>HIGH: {highScore}</Text>}
            {boss && boss.active && (
              <View style={styles.bossHealthContainer}>
                <Text style={styles.bossLabel}>BOSS</Text>
                <View style={styles.bossHealthBar}>
                  <View style={[styles.bossHealthFill, { width: `${(boss.hp / boss.maxHp) * 100}%` }]} />
                </View>
              </View>
            )}
          </View>

          <View style={styles.instructions}>
            <Text style={styles.instructionText}>Swipe to move | Tap to fire | Don't let enemies pass!</Text>
          </View>

          <View style={styles.gameArea}>
            <Text style={styles.gameText}>{renderGrid()}</Text>
          </View>

          {gameOver && (
            <View style={styles.gameOverOverlay}>
              <Text style={styles.gameOverText}>GAME OVER</Text>
              <Text style={styles.finalScore}>Final Score: {score}</Text>
              <Text style={styles.finalWave}>Reached Wave {wave}</Text>
              <Pressable style={styles.button} onPress={resetGame}>
                <Text style={styles.buttonText}>PLAY AGAIN</Text>
              </Pressable>
            </View>
          )}

          <Pressable style={styles.closeButton} onPress={onClose}>
            <Text style={styles.closeButtonText}>X</Text>
          </Pressable>
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.95)',
    justifyContent: 'center',
    alignItems: 'center',
  },
  damageFlash: {
    backgroundColor: 'rgba(255, 0, 0, 0.3)',
  },
  gameContainer: {
    backgroundColor: '#0a0a0a',
    borderRadius: 8,
    padding: 16,
    borderWidth: 2,
    borderColor: '#00ff00',
    maxWidth: '95%',
    position: 'relative',
  },
  header: {
    alignItems: 'center',
    marginBottom: 8,
  },
  title: {
    color: '#00ff00',
    fontSize: 24,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    fontWeight: 'bold',
    textShadowColor: '#00ff00',
    textShadowOffset: { width: 0, height: 0 },
    textShadowRadius: 10,
  },
  statsRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    width: '100%',
    paddingHorizontal: 8,
    marginTop: 4,
  },
  lives: {
    color: '#ff6666',
    fontSize: 16,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
  },
  wave: {
    color: '#66ffff',
    fontSize: 14,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
  },
  score: {
    color: '#00ff00',
    fontSize: 14,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
  },
  combo: {
    color: '#ffff00',
    fontSize: 14,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    fontWeight: 'bold',
  },
  highScore: {
    color: '#ffff00',
    fontSize: 12,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    marginTop: 2,
  },
  bossHealthContainer: {
    width: '100%',
    marginTop: 8,
    alignItems: 'center',
  },
  bossLabel: {
    color: '#ff0066',
    fontSize: 12,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    fontWeight: 'bold',
    marginBottom: 2,
  },
  bossHealthBar: {
    width: '80%',
    height: 8,
    backgroundColor: '#333333',
    borderRadius: 4,
    overflow: 'hidden',
  },
  bossHealthFill: {
    height: '100%',
    backgroundColor: '#ff0066',
  },
  instructions: {
    alignItems: 'center',
    marginBottom: 8,
  },
  instructionText: {
    color: '#666666',
    fontSize: 11,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
  },
  gameArea: {
    backgroundColor: '#000000',
    padding: 8,
    borderRadius: 4,
    borderWidth: 1,
    borderColor: '#333333',
  },
  gameText: {
    color: '#00ff00',
    fontSize: Platform.OS === 'ios' ? 14 : 12,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    lineHeight: Platform.OS === 'ios' ? 16 : 14,
    letterSpacing: 2,
  },
  gameOverOverlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0, 0, 0, 0.85)',
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 8,
  },
  gameOverText: {
    color: '#ff0000',
    fontSize: 32,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    fontWeight: 'bold',
    textShadowColor: '#ff0000',
    textShadowOffset: { width: 0, height: 0 },
    textShadowRadius: 15,
  },
  finalScore: {
    color: '#ffffff',
    fontSize: 18,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    marginTop: 16,
  },
  finalWave: {
    color: '#66ffff',
    fontSize: 14,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    marginTop: 4,
  },
  button: {
    marginTop: 24,
    backgroundColor: '#00ff00',
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 4,
  },
  buttonText: {
    color: '#000000',
    fontSize: 16,
    fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
    fontWeight: 'bold',
  },
  closeButton: {
    position: 'absolute',
    top: 8,
    right: 8,
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: '#333333',
    justifyContent: 'center',
    alignItems: 'center',
  },
  closeButtonText: {
    color: '#ffffff',
    fontSize: 16,
    fontWeight: 'bold',
  },
});
