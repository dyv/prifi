#ifndef DISSENT_CRYPTO_NULL_LIBRARY_H_GUARD
#define DISSENT_CRYPTO_NULL_LIBRARY_H_GUARD

#include "NullDiffieHellman.hpp"
#include "NullHash.hpp"
#include "Utils/Random.hpp"
#include "NullPrivateKey.hpp"
#include "NullPublicKey.hpp"
#include "CppIntegerData.hpp"

#include "Library.hpp"

namespace Dissent {
namespace Crypto {
  class NullLibrary : public Library {
    public:
      /**
       * Load a public key from a file
       */
      inline virtual AsymmetricKey *LoadPublicKeyFromFile(const QString &filename) const
      {
        return new NullPublicKey(filename);
      }

      /**
       * Loading a public key from a byte array
       */
      inline virtual AsymmetricKey *LoadPublicKeyFromByteArray(const QByteArray &data) const
      {
        return new NullPublicKey(data);
      }

      /**
       * Generate a public key using the given data as a seed to a RNG
       */
      inline virtual AsymmetricKey *GeneratePublicKey(const QByteArray &seed) const
      {
        return NullPublicKey::GenerateKey(seed);
      }

      /**
       * Load a private key from a file
       */
      inline virtual AsymmetricKey *LoadPrivateKeyFromFile(const QString &filename) const
      {
        return new NullPrivateKey(filename);
      }

      /**
       * Loading a private key from a byte array
       */
      inline virtual AsymmetricKey *LoadPrivateKeyFromByteArray(const QByteArray &data) const
      {
        return new NullPrivateKey(data);
      }

      /**
       * Generate a private key using the given data as a seed to a RNG
       */
      inline virtual AsymmetricKey *GeneratePrivateKey(const QByteArray &seed) const
      {
        return NullPrivateKey::GenerateKey(seed);
      }

      /**
       * Generates a unique (new) private key
       */
      inline virtual AsymmetricKey *CreatePrivateKey() const
      {
        return new NullPrivateKey();
      }

      /**
       * Returns the minimum asymmetric key size
       */
      inline virtual int MinimumKeySize() const { return NullPublicKey::GetMinimumKeySize(); }

      /**
       * Returns a random number generator
       */
      inline virtual Utils::Random *GetRandomNumberGenerator(const QByteArray &seed) const
      {
        return new Utils::Random(seed);
      }

      inline virtual uint RngOptimalSeedSize() const
      {
        return Utils::Random::OptimalSeedSize();
      }

      /**
       * Returns a hash algorithm
       */
      inline virtual Hash *GetHashAlgorithm() const
      {
        return new NullHash();
      }

      /**
       * Returns an integer data
       */
      inline virtual IntegerData *GetIntegerData(int value) const
      {
        return new CppIntegerData(value);
      }

      /**
       * Returns an integer data
       */
      inline virtual IntegerData *GetIntegerData(const QByteArray &value) const
      {
        return new CppIntegerData(value);
      }

      /**
       * Returns an integer data
       */
      inline virtual IntegerData *GetIntegerData(const QString &value) const
      {
        return new CppIntegerData(value);
      }

      /**
       * returns a random integer data
       * @param bit_count the amount of bits in the integer
       * @param prime if the integer should be prime 
       */
      virtual IntegerData *GetRandomInteger(int bit_count, bool prime) const
      {
        return CppIntegerData::GetRandomInteger(bit_count, prime);
      }

      /**
       * returns a random integer data
       * @param min the minimum number
       * @param max the maximum number
       * @param prime should the resulting number be prime
       */
      virtual IntegerData *GetRandomInteger(const IntegerData *min,
          const IntegerData *max, bool prime) const
      {
        return CppIntegerData::GetRandomInteger(min, max, prime);
      }

      /**
       * Returns a DiffieHellman operator
       */
      virtual DiffieHellman *CreateDiffieHellman() const
      {
        return new NullDiffieHellman();
      }

      /**
       * Generate a DiffieHellman operator using the given data as a seed to a RNG
       * @param seed seed used to generate the DiffieHellman exchange
       */
      virtual DiffieHellman *GenerateDiffieHellman(const QByteArray &seed) const
      {
        return NullDiffieHellman::GenerateFromSeed(seed);
      }

      /**
       * Loads a DiffieHellman key from a byte array
       * @param private_component the private component in the DH exchange
       */
      virtual DiffieHellman *LoadDiffieHellman(const QByteArray &private_component) const
      {
        return new NullDiffieHellman(private_component);
      }
  };
}
}

#endif
