package backoff

import (
	"math"
	"math/rand"
	"time"
)

// Config controla el backoff exponencial con jitter.
type Config struct {
	Initial time.Duration // primera espera tras un fallo
	Max     time.Duration // límite superior de espera
	Factor  float64       // multiplicador por intento (ej. 2.0)
}

// Default es una config razonable para reconexión de red.
func Default() Config {
	return Config{
		Initial: 500 * time.Millisecond,
		Max:     30 * time.Second,
		Factor:  2.0,
	}
}

// Next devuelve la próxima espera dado el intento (0-indexado), con jitter.
// El jitter lo hace aleatorio dentro de [0, 1) para evitar que todos los
// clientes reintenten al mismo tiempo.
func (c Config) Next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	exp := c.Initial.Seconds() * math.Pow(c.Factor, float64(attempt))
	if exp > c.Max.Seconds() {
		exp = c.Max.Seconds()
	}
	jittered := exp * (0.5 + rand.Float64()*0.5) // 50-100% del valor
	if jittered < c.Initial.Seconds() {
		jittered = c.Initial.Seconds()
	}
	return time.Duration(jittered * float64(time.Second))
}
