import { useRequest } from '@umijs/max';

type RawRequestResult<T, P extends any[]> = {
  data?: T;
  loading: boolean;
  error?: Error;
  refresh: () => void;
  run: (...params: P) => void;
  runAsync?: (...params: P) => Promise<T>;
  mutate: (data?: T) => void;
} & Record<string, any>;

export function useRawRequest<T, P extends any[] = []>(
  service: (...params: P) => Promise<T>,
  options?: Record<string, any>,
) {
  return useRequest(service as any, {
    formatResult: (result: T) => result,
    ...options,
  }) as unknown as RawRequestResult<T, P>;
}
