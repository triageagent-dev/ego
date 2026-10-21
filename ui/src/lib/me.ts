import { useEffect, useState } from 'react';
import { config } from '../config';

export interface Me {
  authenticated: boolean;
  // false when the server runs without auth: no login to offer.
  authEnabled?: boolean;
  username?: string;
  role?: string;
}

// Who is signed in, asked once per page load; null until the server has answered.
export function useMe(): Me | null {
  const [me, setMe] = useState<Me | null>(null);
  useEffect(() => {
    let active = true;
    fetch(config.api.endpoints.authMe)
      .then((res) => (res.ok ? res.json() : { authenticated: false }))
      .then((data) => active && setMe(data))
      .catch(() => active && setMe({ authenticated: false }));
    return () => {
      active = false;
    };
  }, []);
  return me;
}
